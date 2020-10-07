package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/store"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

var (
	errEventsBeforeClientInitialized = errors.New("Relay is not ready to process events yet (data store not yet created)") //nolint:stylecheck
)

func errUnknownEventKind(kind string) error {
	return fmt.Errorf("unexpected event kind: %s", kind)
}

type eventSummarizingRelay struct {
	eventProcessor ldevents.EventProcessor
	storeAdapter   *store.SSERelayDataStoreAdapter
	eventSender    *reconfigurableEventSender
	loggers        ldlog.Loggers
}

type reconfigurableEventSender struct {
	eventSender ldevents.EventSender
	httpClient  *http.Client
	eventsURI   string
	headers     http.Header
	loggers     ldlog.Loggers
	lock        sync.Mutex
}

type receivedEventUser struct {
	lduser.User
	PrivateAttrs []string `json:"privateAttrs"`
}

func (r receivedEventUser) asEventUser() ldevents.EventUser {
	return ldevents.EventUser{User: r.User, AlreadyFilteredAttributes: r.PrivateAttrs}
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

func newEventSummarizingRelay(
	config c.EventsConfig,
	httpConfig httpconfig.HTTPConfig,
	credential c.SDKCredential,
	storeAdapter *store.SSERelayDataStoreAdapter,
	loggers ldlog.Loggers,
	remotePath string,
) *eventSummarizingRelay {
	httpClient := httpConfig.SDKHTTPConfig.CreateHTTPClient()
	headers := httpConfig.SDKHTTPConfig.GetDefaultHeaders()
	headers.Set("Authorization", credential.GetAuthorizationHeaderValue())
	eventsURI := strings.TrimRight(getEventsURI(config), "/") + remotePath
	eventSender := newReconfigurableEventSender(httpClient, eventsURI, headers, loggers)

	eventsConfig := ldevents.EventsConfiguration{
		Capacity:              config.Capacity.GetOrElse(c.DefaultEventCapacity),
		InlineUsersInEvents:   config.InlineUsers,
		EventSender:           eventSender,
		FlushInterval:         config.FlushInterval.GetOrElse(c.DefaultEventsFlushInterval),
		Loggers:               loggers,
		UserKeysCapacity:      ldcomponents.DefaultUserKeysCapacity,
		UserKeysFlushInterval: ldcomponents.DefaultUserKeysFlushInterval,
	}
	ep := ldevents.NewDefaultEventProcessor(eventsConfig)

	return &eventSummarizingRelay{
		eventProcessor: ep,
		storeAdapter:   storeAdapter,
		eventSender:    eventSender,
		loggers:        loggers,
	}
}

func (er *eventSummarizingRelay) enqueue(rawEvents []json.RawMessage, schemaVersion int) bool {
	for _, rawEvent := range rawEvents {
		evt, err := er.translateEvent(rawEvent, schemaVersion)
		if err != nil {
			er.loggers.Errorf("Error in event processing, event was discarded: %s", err)
			continue
		}
		if evt != nil {
			switch e := evt.(type) {
			case ldevents.FeatureRequestEvent:
				er.eventProcessor.RecordFeatureRequestEvent(e)
			case ldevents.IdentifyEvent:
				er.eventProcessor.RecordIdentifyEvent(e)
			case ldevents.CustomEvent:
				er.eventProcessor.RecordCustomEvent(e)
			}
		}
	}
	return true
}

func (er *eventSummarizingRelay) translateEvent(rawEvent json.RawMessage, schemaVersion int) (ldevents.Event, error) {
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
	}
	return nil, errUnknownEventKind(kindFieldOnly.Kind)
}

func (er *eventSummarizingRelay) close() {
	_ = er.eventProcessor.Close()
}

func newReconfigurableEventSender(
	httpClient *http.Client,
	eventsURI string,
	headers http.Header,
	loggers ldlog.Loggers,
) *reconfigurableEventSender {
	eventSender := ldevents.NewDefaultEventSender(httpClient, eventsURI, "", headers, loggers)
	return &reconfigurableEventSender{
		eventSender: eventSender,
		httpClient:  httpClient,
		eventsURI:   eventsURI,
		headers:     headers,
		loggers:     loggers,
	}
}

func (r *reconfigurableEventSender) replaceCredential(newCredential c.SDKCredential) {
	r.lock.Lock()
	defer r.lock.Unlock()
	headers := make(http.Header)
	for k, v := range r.headers {
		headers[k] = v
	}
	headers.Set("Authorization", newCredential.GetAuthorizationHeaderValue())
	r.headers = headers
	r.eventSender = ldevents.NewDefaultEventSender(r.httpClient, r.eventsURI, "", r.headers, r.loggers)
}

func (r *reconfigurableEventSender) SendEventData(kind ldevents.EventDataKind, data []byte, count int) ldevents.EventSenderResult {
	r.lock.Lock()
	sender := r.eventSender
	r.lock.Unlock()
	return sender.SendEventData(kind, data, count)
}
