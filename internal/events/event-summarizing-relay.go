package events

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	ld "gopkg.in/launchdarkly/go-server-sdk.v4"

	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

type eventSummarizingRelay struct {
	eventProcessor ld.EventProcessor
	featureStore   ld.FeatureStore
}

func newEventSummarizingRelay(sdkKey string, config Config, featureStore ld.FeatureStore, remotePath string) *eventSummarizingRelay {
	ldConfig := ld.DefaultConfig
	ldConfig.EventsEndpointUri = strings.TrimRight(config.EventsUri, "/") + remotePath
	ldConfig.Capacity = config.Capacity
	ldConfig.InlineUsersInEvents = config.InlineUsers
	ldConfig.FlushInterval = time.Duration(config.FlushIntervalSecs) * time.Second
	ldConfig.HTTPAdapter = nil
	ep := ld.NewDefaultEventProcessor(sdkKey, ldConfig, nil)
	return &eventSummarizingRelay{
		eventProcessor: ep,
		featureStore:   featureStore,
	}
}

func (er *eventSummarizingRelay) enqueue(rawEvents []json.RawMessage, schemaVersion int) {
	for _, rawEvent := range rawEvents {
		var fields map[string]interface{}
		err := json.Unmarshal(rawEvent, &fields)
		if err == nil {
			evt, err := er.translateEvent(rawEvent, fields, schemaVersion)
			if err != nil {
				logging.Error.Printf("Error in event processing, event was discarded: %+v", err)
			}
			if evt != nil {
				er.eventProcessor.SendEvent(evt)
			}
		}
	}
}

func (er *eventSummarizingRelay) translateEvent(rawEvent json.RawMessage, fields map[string]interface{}, schemaVersion int) (ld.Event, error) {
	switch fields["kind"] {
	case ld.FeatureRequestEventKind:
		var e ld.FeatureRequestEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		// Look up the feature flag so we can set the event's TrackEvent and DebugEventsUntilDate properties
		// (unless Version was omitted, which means the flag did't exist).
		if e.Version == nil {
			e.Variation = nil
		} else {
			data, err := er.featureStore.Get(ld.Features, e.Key)
			if err != nil {
				return nil, err
			}
			if data != nil {
				flag := data.(*ld.FeatureFlag)
				e.TrackEvents = flag.TrackEvents
				e.DebugEventsUntilDate = flag.DebugEventsUntilDate
				// If schemaVersion is 1, this is from an old SDK that doesn't send a variation index, so we
				// have to infer the index from the value. Schema version 2 is a newer PHP SDK that does send
				// a variation index.
				if schemaVersion == 1 {
					for i, value := range flag.Variations {
						if reflect.DeepEqual(value, e.Value) {
							e.Variation = &i
							break
						}
					}
				}
			}
		}
		if err != nil {
			return nil, err
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
	return nil, fmt.Errorf("unexpected event kind: %s", fields["kind"])
}
