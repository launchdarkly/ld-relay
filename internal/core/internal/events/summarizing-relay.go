package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/store"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

var (
	errEventsBeforeClientInitialized = errors.New("Relay is not ready to process events yet (data store not yet created)") //nolint:stylecheck
)

func errUnknownEventKind(kind string) error {
	return fmt.Errorf("unexpected event kind: %s", kind)
}

// eventSummarizingRelay is a component that takes events received from a PHP SDK, in event schema 2--
// where private attribute redaction has already been done, but summary event computation and user
// deduplication has not been done-- and feeds them through the Go SDK's EventProcessor to produce
// the final event data in the normal schema to be sent to LaunchDarkly. The EventProcessor takes care
// of flushing the final events at the configured interval.
//
// Like HTTPEventPublisher, this supports proxying events in separate payloads if we received them
// with different request metadata (e.g. tags). To do this, we have to maintain a separate
// EventProcessor instance for each unique metadata set we've seen.
type eventSummarizingRelay struct {
	queues       map[EventPayloadMetadata]*eventSummarizingRelayQueue
	authKey      c.SDKCredential
	httpClient   *http.Client
	baseHeaders  http.Header
	storeAdapter *store.SSERelayDataStoreAdapter
	eventsConfig ldevents.EventsConfiguration
	eventsURI    string
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

type receivedEventUser struct {
	eventUser ldevents.EventUser
}

func (r *receivedEventUser) UnmarshalJSON(data []byte) error {
	var user lduser.User
	if err := json.Unmarshal(data, &user); err != nil {
		return err
	}
	var extraProps struct {
		PrivateAttrs []string `json:"privateAttrs"`
	}
	if strings.Contains(string(data), `"privateAttrs"`) {
		if err := json.Unmarshal(data, &extraProps); err != nil {
			return err
		}
	}
	r.eventUser.User = user
	r.eventUser.AlreadyFilteredAttributes = extraProps.PrivateAttrs
	return nil
}

func (r receivedEventUser) asEventUser() ldevents.EventUser {
	return r.eventUser
}

type receivedFeatureEvent struct {
	CreationDate         ldtime.UnixMillisecondTime `json:"creationDate"`
	Key                  string                     `json:"key"`
	User                 receivedEventUser          `json:"user"`
	Version              ldvalue.OptionalInt        `json:"version"`
	Variation            ldvalue.OptionalInt        `json:"variation"`
	Value                ldvalue.Value              `json:"value"`
	Default              ldvalue.Value              `json:"default"`
	TrackEvents          bool                       `json:"trackEvents"`
	DebugEventsUntilDate ldtime.UnixMillisecondTime `json:"debugEventsUntilDate"`
	Reason               ldreason.EvaluationReason  `json:"reason"`
}

type receivedCustomEvent struct {
	CreationDate ldtime.UnixMillisecondTime `json:"creationDate"`
	Key          string                     `json:"key"`
	User         receivedEventUser          `json:"user"`
	Data         ldvalue.Value              `json:"data"`
	MetricValue  *float64                   `json:"metricValue"`
}

type receivedIdentifyEvent struct {
	CreationDate ldtime.UnixMillisecondTime `json:"creationDate"`
	User         receivedEventUser          `json:"user"`
}

type receivedAliasEvent struct {
	CreationDate ldtime.UnixMillisecondTime `json:"creationDate"`
	CurrentKey   string                     `json:"key"`
	CurrentKind  string                     `json:"contextKind"`
	PreviousKey  string                     `json:"previousKey"`
	PreviousKind string                     `json:"previousContextKind"`
	// Note that the JSON property names here aren't quite the same as the logical property names - the
	// former are what are really used in the JSON data, the latter correspond to how they're described
	// in the ldevents API
}

func newEventSummarizingRelay(
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	credential c.SDKCredential,
	storeAdapter *store.SSERelayDataStoreAdapter,
	loggers ldlog.Loggers,
	remotePath string,
	eventQueueCleanupInterval time.Duration,
) *eventSummarizingRelay {
	eventsConfig := ldevents.EventsConfiguration{
		Capacity:              config.Capacity.GetOrElse(c.DefaultEventCapacity),
		InlineUsersInEvents:   config.InlineUsers,
		FlushInterval:         config.FlushInterval.GetOrElse(c.DefaultEventsFlushInterval),
		Loggers:               loggers,
		UserKeysCapacity:      ldcomponents.DefaultUserKeysCapacity,
		UserKeysFlushInterval: ldcomponents.DefaultUserKeysFlushInterval,
	}
	er := &eventSummarizingRelay{
		queues:       make(map[EventPayloadMetadata]*eventSummarizingRelayQueue),
		authKey:      credential,
		httpClient:   httpConfig.SDKHTTPConfig.CreateHTTPClient(),
		baseHeaders:  httpConfig.SDKHTTPConfig.GetDefaultHeaders(),
		storeAdapter: storeAdapter,
		eventsConfig: eventsConfig,
		eventsURI:    strings.TrimRight(getEventsURI(config), "/") + remotePath,
		loggers:      loggers,
		closer:       make(chan struct{}),
	}
	go er.runPeriodicCleanupTaskUntilClosed(eventQueueCleanupInterval)
	return er
}

func (er *eventSummarizingRelay) enqueue(metadata EventPayloadMetadata, rawEvents []json.RawMessage) bool {
	er.lock.Lock()
	if er.queues == nil {
		// this instance has been shut down
		er.lock.Unlock()
		return false
	}
	queue := er.queues[metadata]
	if queue == nil {
		sender := &delegatingEventSender{
			wrapped: makeEventSender(er.httpClient, er.eventsURI, er.baseHeaders, er.authKey, metadata, er.loggers),
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
		evt, err := er.translateEvent(rawEvent, metadata.SchemaVersion)
		if err != nil {
			er.loggers.Errorf("Error in event processing, event was discarded: %s", err)
			continue
		}
		if evt != nil {
			switch e := evt.(type) {
			case ldevents.FeatureRequestEvent:
				queue.eventProcessor.RecordFeatureRequestEvent(e)
			case ldevents.IdentifyEvent:
				queue.eventProcessor.RecordIdentifyEvent(e)
			case ldevents.CustomEvent:
				queue.eventProcessor.RecordCustomEvent(e)
			case ldevents.AliasEvent:
				queue.eventProcessor.RecordAliasEvent(e)
			}
		}
	}
	return true
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

func (er *eventSummarizingRelay) replaceCredential(newCredential c.SDKCredential) {
	er.lock.Lock()
	if reflect.TypeOf(newCredential) == reflect.TypeOf(er.authKey) {
		er.authKey = newCredential
		for metadata, queue := range er.queues {
			sender := makeEventSender(er.httpClient, er.eventsURI, er.baseHeaders, newCredential, metadata, er.loggers)
			queue.eventSender.setWrapped(sender)
		}
	}
	er.lock.Unlock()
}

func (er *eventSummarizingRelay) translateEvent(rawEvent json.RawMessage, schemaVersion int) (interface{}, error) {
	var kindFieldOnly struct {
		Kind string
	}
	if err := json.Unmarshal(rawEvent, &kindFieldOnly); err != nil {
		return nil, err
	}
	switch kindFieldOnly.Kind {
	case ldevents.FeatureRequestEventKind:
		var e receivedFeatureEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}

		newEvent := ldevents.FeatureRequestEvent{
			BaseEvent: ldevents.BaseEvent{
				CreationDate: e.CreationDate,
				User:         e.User.asEventUser(),
			},
			Key:     e.Key,
			Version: e.Version,
			Value:   e.Value,
			Default: e.Default,
			Reason:  e.Reason,
		}

		// There are three possible cases we need to handle here.
		// 1. This is from an old SDK, prior to the implementation of summary events. SchemaVersion will be 1
		//    (or omitted). We have to look up the flag, get the TrackEvents and DebugEventsUntilDate properties
		//    from the flag, and also infer VariationIndex from the flag value (since old SDKs did not set this).
		// 2a. This is from a PHP SDK version >= 3.1.0 but < 3.6.0. SchemaVersion will be 2. The SDK does set
		//    VariationIndex, but it does not set TrackEvents or DebugEventsUntilDate, so we have to look up the
		//    flag for those.
		// 2b. PHP SDK version >= 3.6.0 does set TrackEvents and DebugEventsUntilDate for us. Unfortunately, we
		//    cannot distinguish an event that has false/null in these properties because that is their value
		//    from an event that simply didn't have them set because the SDK didn't know to set them; the
		//    schemaVersion will be 2 in either case. So, if they are false/null, then we have to look up the
		//    flag to get them. But if they do have values in the event, we must respect those (since they may
		//    have been determined by experimentation logic rather than the top-level flag properties).
		// However, if the Version property was not provided in the original event, then the flag didn't exist
		// so we won't bother doing any of that; whatever's in the event is all we have.
		if e.Version.IsDefined() {
			newEvent.Variation = e.Variation
			if e.TrackEvents || e.DebugEventsUntilDate != 0 {
				newEvent.TrackEvents = e.TrackEvents
				newEvent.DebugEventsUntilDate = e.DebugEventsUntilDate
				return newEvent, nil // case 2b - we know this is a newer PHP SDK if these properties have truthy values
			}
			store := er.storeAdapter.GetStore()
			if store == nil {
				// The data store has not been created yet. That is pretty much the first thing that happens when
				// the LDClient is created, and the LDClient is created when Relay starts up, so this can only happen
				// if we receive events very early during startup. There's nothing we can do about this, and it's not
				// terribly significant because if the SDK had sent the events a few milliseconds earlier, Relay
				// would've been even less ready to receive them.
				return nil, errEventsBeforeClientInitialized // COVERAGE: no good way to make this happen in unit tests currently
			}
			// it's case 1 (very old SDK), 2a (older PHP SDK), or 2b (newer PHP, but the properties don't happen
			// to be set so we can't distinguish it from 2a and must look up the flag)
			data, err := store.Get(ldstoreimpl.Features(), e.Key)
			if err != nil {
				return nil, err // COVERAGE: no good way to make this happen in unit tests currently
			}
			if data.Item != nil {
				flag := data.Item.(*ldmodel.FeatureFlag)
				newEvent.TrackEvents = flag.TrackEvents
				newEvent.DebugEventsUntilDate = flag.DebugEventsUntilDate
				if schemaVersion <= 1 && !e.Variation.IsDefined() {
					for i, value := range flag.Variations {
						if value.Equal(e.Value) {
							n := i
							newEvent.Variation = ldvalue.NewOptionalInt(n)
							break
						}
					}
				}
			}
		}
		return newEvent, nil
	case ldevents.CustomEventKind:
		var e receivedCustomEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		newEvent := ldevents.CustomEvent{
			BaseEvent: ldevents.BaseEvent{
				CreationDate: e.CreationDate,
				User:         e.User.asEventUser(),
			},
			Key:  e.Key,
			Data: e.Data,
		}
		if e.MetricValue != nil {
			newEvent.HasMetric = true
			newEvent.MetricValue = *e.MetricValue
		}
		return newEvent, nil
	case ldevents.IdentifyEventKind:
		var e receivedIdentifyEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		return ldevents.IdentifyEvent{
			BaseEvent: ldevents.BaseEvent{
				CreationDate: e.CreationDate,
				User:         e.User.asEventUser(),
			},
		}, nil
	case ldevents.AliasEventKind:
		var e receivedAliasEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		return ldevents.AliasEvent{
			CreationDate: e.CreationDate,
			CurrentKey:   e.CurrentKey,
			CurrentKind:  e.CurrentKind,
			PreviousKey:  e.PreviousKey,
			PreviousKind: e.PreviousKind,
		}, nil
	}
	return nil, errUnknownEventKind(kindFieldOnly.Kind)
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
			er.loggers.Debug("Checking for inactive summarizing relay instances")
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
