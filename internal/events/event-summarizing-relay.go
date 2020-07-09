package events

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"

	"github.com/launchdarkly/ld-relay/v6/httpconfig"
)

type eventSummarizingRelay struct {
	eventProcessor ld.EventProcessor
	featureStore   ld.FeatureStore
	loggers        ldlog.Loggers
}

func newEventSummarizingRelay(sdkKey string, config Config, httpConfig httpconfig.HTTPConfig, featureStore ld.FeatureStore,
	loggers ldlog.Loggers, remotePath string) *eventSummarizingRelay {
	ldConfig := ld.DefaultConfig
	ldConfig.EventsEndpointUri = strings.TrimRight(config.EventsUri, "/") + remotePath
	ldConfig.Capacity = config.Capacity
	ldConfig.InlineUsersInEvents = config.InlineUsers
	ldConfig.FlushInterval = time.Duration(config.FlushIntervalSecs) * time.Second
	ldConfig.HTTPClientFactory = httpConfig.HTTPClientFactory
	ldConfig.Loggers = loggers
	ep := ld.NewDefaultEventProcessor(sdkKey, ldConfig, nil)
	return &eventSummarizingRelay{
		eventProcessor: ep,
		featureStore:   featureStore,
		loggers:        loggers,
	}
}

func (er *eventSummarizingRelay) enqueue(rawEvents []json.RawMessage, schemaVersion int) {
	for _, rawEvent := range rawEvents {
		evt, err := er.translateEvent(rawEvent, schemaVersion)
		if err != nil {
			er.loggers.Errorf("Error in event processing, event was discarded: %+v", err)
		}
		if evt != nil {
			er.eventProcessor.SendEvent(evt)
		}
	}
}

func (er *eventSummarizingRelay) translateEvent(rawEvent json.RawMessage, schemaVersion int) (ld.Event, error) {
	var kindFieldOnly struct {
		Kind string
	}
	if err := json.Unmarshal(rawEvent, &kindFieldOnly); err != nil {
		return nil, err
	}
	switch kindFieldOnly.Kind {
	case ld.FeatureRequestEventKind:
		var e ld.FeatureRequestEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
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
		if e.Version == nil {
			// If Version was omitted, then the flag didn't exist so we won't bother looking it up. Whatever's
			// in the event is all we have.
			e.Variation = nil
		} else {
			if e.TrackEvents || e.DebugEventsUntilDate != nil {
				return e, nil // case 2b - we know this is a newer PHP SDK if these properties have truthy values
			}
			// it's case 1 (very old SDK), 2a (older PHP SDK), or 2b (newer PHP, but the properties don't happen
			// to be set so we can't distinguish it from 2a and must look up the flag)
			data, err := er.featureStore.Get(ld.Features, e.Key)
			if err != nil {
				return nil, err
			}
			if data != nil {
				flag := data.(*ld.FeatureFlag)
				e.TrackEvents = flag.TrackEvents
				e.DebugEventsUntilDate = flag.DebugEventsUntilDate
				if schemaVersion <= 1 && e.Variation == nil {
					for i, value := range flag.Variations {
						if reflect.DeepEqual(value, e.Value) {
							n := i
							e.Variation = &n
							break
						}
					}
				}
			}
		}
		return e, nil
	case ld.CustomEventKind:
		var e ld.CustomEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		return e, nil
	case ld.IdentifyEventKind:
		var e ld.IdentifyEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		return e, nil
	}
	return nil, fmt.Errorf("unexpected event kind: %s", kindFieldOnly.Kind)
}
