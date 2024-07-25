package events

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/events/oldevents"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/store"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
)

// eventSummarizingRelay is a component that takes events received from a PHP SDK, in event schema 2--
// where private attribute redaction has already been done, but summary event computation and user
// deduplication has not been done-- and feeds them through the Go SDK's EventProcessor to produce
// the final event data in the normal schema to be sent to LaunchDarkly. The EventProcessor takes care
// of flushing the final events at the configured interval.
//
// See package comments in the oldevents package for more information on how we process these events.
//
// Like HTTPEventPublisher, this supports proxying events in separate payloads if we received them
// with different request metadata (e.g. tags). To do this, we have to maintain a separate
// EventProcessor instance for each unique metadata set we've seen.
type eventSummarizingRelay struct {
	queues       map[EventPayloadMetadata]*eventSummarizingRelayQueue
	authKey      credential.SDKCredential
	httpClient   *http.Client
	baseHeaders  http.Header
	storeAdapter *store.SSERelayDataStoreAdapter
	eventsConfig ldevents.EventsConfiguration
	baseURI      string
	remotePath   string
	loggers      ldlog.Loggers
	closer       chan struct{}
	lock         sync.Mutex
	closeOnce    sync.Once
}

type eventSummarizingRelayQueue struct {
	metadata       EventPayloadMetadata
	eventProcessor ldevents.EventProcessor
	eventSender    *delegatingEventSender
	active         bool
}

type delegatingEventSender struct {
	wrapped ldevents.EventSender
	lock    sync.Mutex
}

func newEventSummarizingRelay(
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	credential credential.SDKCredential,
	storeAdapter *store.SSERelayDataStoreAdapter,
	loggers ldlog.Loggers,
	remotePath string,
	eventQueueCleanupInterval time.Duration,
) *eventSummarizingRelay {
	eventsConfig := ldevents.EventsConfiguration{
		Capacity:              config.Capacity.GetOrElse(c.DefaultEventCapacity),
		FlushInterval:         config.FlushInterval.GetOrElse(c.DefaultEventsFlushInterval),
		Loggers:               loggers,
		UserKeysCapacity:      ldcomponents.DefaultContextKeysCapacity,
		UserKeysFlushInterval: ldcomponents.DefaultContextKeysFlushInterval,
	}
	baseHeaders := make(http.Header)
	for k, v := range httpConfig.SDKHTTPConfig.DefaultHeaders {
		baseHeaders[k] = v
	}
	baseHeaders.Del("Authorization") // we'll set this in makeEventSender()
	er := &eventSummarizingRelay{
		queues:       make(map[EventPayloadMetadata]*eventSummarizingRelayQueue),
		authKey:      credential,
		httpClient:   httpConfig.SDKHTTPConfig.CreateHTTPClient(),
		baseHeaders:  baseHeaders,
		storeAdapter: storeAdapter,
		eventsConfig: eventsConfig,
		baseURI:      getEventsURI(config),
		remotePath:   remotePath,
		loggers:      loggers,
		closer:       make(chan struct{}),
	}
	go er.runPeriodicCleanupTaskUntilClosed(eventQueueCleanupInterval)
	return er
}

func (er *eventSummarizingRelay) enqueue(metadata EventPayloadMetadata, rawEvents []json.RawMessage) {
	er.lock.Lock()
	if er.queues == nil {
		// this instance has been shut down
		er.lock.Unlock()
		return
	}
	queue := er.queues[metadata]
	if queue == nil {
		sender := &delegatingEventSender{
			wrapped: makeEventSender(er.httpClient, er.baseURI, er.remotePath, er.baseHeaders, er.authKey, metadata, er.loggers),
		}
		eventsConfig := er.eventsConfig
		eventsConfig.EventSender = sender
		queue = &eventSummarizingRelayQueue{
			metadata:       metadata,
			eventSender:    sender,
			eventProcessor: ldevents.NewDefaultEventProcessor(eventsConfig),
		}
		er.queues[metadata] = queue
	}
	queue.active = true // see runPeriodicCleanupTaskUntilClosed()
	er.lock.Unlock()

	for _, rawEvent := range rawEvents {
		oldEvent, err := oldevents.UnmarshalEvent(rawEvent)
		if err != nil {
			er.loggers.Errorf("Error in event processing, event was discarded: %s", err)
			continue
		}
		_ = er.dispatchEvent(queue.eventProcessor, oldEvent, rawEvent, metadata.SchemaVersion)
	}
}

func (er *eventSummarizingRelay) flush() { //nolint:unused // used only in tests
	processors := make([]ldevents.EventProcessor, 0, 10) // arbitrary initial capacity
	er.lock.Lock()
	for _, queue := range er.queues {
		processors = append(processors, queue.eventProcessor)
	}
	er.lock.Unlock()
	for _, p := range processors {
		p.Flush()
	}
}

func (er *eventSummarizingRelay) replaceCredential(newCredential credential.SDKCredential) {
	er.lock.Lock()
	if reflect.TypeOf(newCredential) == reflect.TypeOf(er.authKey) {
		er.authKey = newCredential
		for metadata, queue := range er.queues {
			// See comment on makeEventSender() about why we create a new one in this situation.
			sender := makeEventSender(er.httpClient, er.baseURI, er.remotePath, er.baseHeaders, newCredential, metadata, er.loggers)
			queue.eventSender.setWrapped(sender)
		}
	}
	er.lock.Unlock()
}

func (er *eventSummarizingRelay) dispatchEvent(
	ep ldevents.EventProcessor,
	oldEvent oldevents.OldEvent,
	rawEvent []byte,
	schemaVersion int,
) error {
	switch e := oldEvent.(type) {
	case oldevents.FeatureEvent:
		evalData, err := oldevents.TranslateFeatureEvent(e, schemaVersion, er.storeAdapter.GetStore())
		if err != nil {
			return err
		}
		ep.RecordEvaluation(evalData)

	case oldevents.CustomEvent:
		customData, err := oldevents.TranslateCustomEvent(e)
		if err != nil {
			return err
		}
		ep.RecordCustomEvent(customData)

	case oldevents.IdentifyEvent:
		identifyData, err := oldevents.TranslateIdentifyEvent(e)
		if err != nil {
			return err
		}
		ep.RecordIdentifyEvent(identifyData)

	case oldevents.UntranslatedEvent:
		// We use this for alias events, and anything else we don't recognize. We can't do any kind of
		// post-processing on such things, so we'll just throw them into the output as-is and let
		// event-recorder decide what to do with them.
		ep.RecordRawEvent(rawEvent)
	}
	return nil
}

func (er *eventSummarizingRelay) runPeriodicCleanupTaskUntilClosed(cleanupInterval time.Duration) {
	// We maintain a separate EventProcessor instance for each unique metadata set we've seen. To
	// avoid accumulating zombie instances if some unique value was seen once but then not seen
	// again, we periodically check whether each instance has received any events since the last
	// check, and shut it down if not. A new one will be created later if necessary by
	// eventSummarizingRelay.enqueue()-- therefore, to avoid the overhead of constantly setting
	// up and tearing down instances (since EventProcessor maintains a lot of state), we normally
	// use a long interval for this check.
	if cleanupInterval == 0 {
		cleanupInterval = defaultEventQueueCleanupInterval
		if cleanupInterval < er.eventsConfig.FlushInterval {
			cleanupInterval = er.eventsConfig.FlushInterval * 2
		}
	}
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-er.closer:
			er.lock.Lock()
			queues := er.queues
			er.queues = nil
			er.lock.Unlock()
			for _, queue := range queues {
				_ = queue.eventProcessor.Close()
			}
			return

		case <-ticker.C:
			unused := make([]*eventSummarizingRelayQueue, 0, 10) // arbitrary initial capacity
			er.lock.Lock()
			if len(er.queues) <= 1 {
				// We'll only bother doing this cleanup logic if more than one instance exists, since the
				// most common use case would be that there is no metadata or that it's always the same.
				er.lock.Unlock()
				continue
			}
			for _, queue := range er.queues {
				if !queue.active {
					unused = append(unused, queue)
				} else {
					queue.active = false // reset it, will recheck at next tick
				}
			}
			for _, queue := range unused {
				delete(er.queues, queue.metadata)
			}
			er.lock.Unlock()

			for _, queue := range unused {
				er.loggers.Debugf("Shutting down inactive summarizing relay for %+v", queue.metadata)
				_ = queue.eventProcessor.Close()
			}
		}
	}
}

func (er *eventSummarizingRelay) close() {
	er.closeOnce.Do(func() {
		er.closer <- struct{}{}
	})
}

func (d *delegatingEventSender) SendEventData(kind ldevents.EventDataKind, data []byte, count int) ldevents.EventSenderResult {
	d.lock.Lock()
	sender := d.wrapped
	d.lock.Unlock()
	return sender.SendEventData(kind, data, count)
}

func (d *delegatingEventSender) setWrapped(newWrappedSender ldevents.EventSender) {
	d.lock.Lock()
	d.wrapped = newWrappedSender
	d.lock.Unlock()
}

// makeEventSender creates a new instance of the EventSender component that is provided by go-sdk-events,
// configuring it to have the appropriate HTTP request headers.
//
// This component provides the standard behavior for error handling and retries on event posts. It does
// not create its own goroutine or HTTP client or do any computations other than the minimum needed to
// send each request, so there's not much overhead to creating and disposing of instances. And since the
// current implementation doesn't allow the configured headers to be changed on a per-request basis, it's
// simplest for us to just create a new instance if the relevant configuration may have changed.
func makeEventSender(
	httpClient *http.Client,
	eventsURI string,
	remotePath string,
	baseHeaders http.Header,
	authKey credential.SDKCredential,
	metadata EventPayloadMetadata,
	loggers ldlog.Loggers,
) ldevents.EventSender {
	headers := make(http.Header)
	for k, v := range baseHeaders {
		headers[k] = v
	}
	if metadata.Tags != "" {
		headers.Set(TagsHeader, metadata.Tags)
	}
	if authKey.GetAuthorizationHeaderValue() != "" {
		headers.Set("Authorization", authKey.GetAuthorizationHeaderValue())
	}
	return &eventSenderWithOverridePath{
		config: ldevents.EventSenderConfiguration{
			Client:            httpClient,
			BaseURI:           eventsURI,
			BaseHeaders:       func() http.Header { return headers },
			Loggers:           loggers,
			EnableCompression: true,
		},
		remotePath: remotePath,
	}
}

type eventSenderWithOverridePath struct {
	config     ldevents.EventSenderConfiguration
	remotePath string
}

func (e *eventSenderWithOverridePath) SendEventData(kind ldevents.EventDataKind, data []byte, eventCount int) ldevents.EventSenderResult {
	return ldevents.SendEventDataWithRetry(e.config, kind, e.remotePath, data, eventCount)
}
