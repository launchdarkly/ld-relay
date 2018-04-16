package main

import (
	"encoding/json"
	"fmt"
	"reflect"

	ld "gopkg.in/launchdarkly/go-client.v3"
)

type eventSummarizingRelay struct {
	eventProcessor ld.EventProcessor
	featureStore   ld.FeatureStore
}

func newEventSummarizingRelay(sdkKey string, config Config, featureStore ld.FeatureStore) *eventSummarizingRelay {
	ldConfig := ld.DefaultConfig
	ldConfig.EventsUri = config.Events.EventsUri
	ldConfig.Capacity = config.Events.Capacity
	ep := ld.NewDefaultEventProcessor(sdkKey, ldConfig, nil)
	return &eventSummarizingRelay{
		eventProcessor: ep,
		featureStore:   featureStore,
	}
}

func (er *eventSummarizingRelay) enqueue(rawEvents []json.RawMessage) {
	for _, rawEvent := range rawEvents {
		var fields map[string]interface{}
		err := json.Unmarshal(rawEvent, &fields)
		if err == nil {
			evt, err := er.translateEvent(rawEvent, fields)
			if err != nil {
				Error.Printf("Error in event processing, event was discarded: %+v", err)
			}
			if evt != nil {
				er.eventProcessor.SendEvent(evt)
			}
		}
	}
}

func (er *eventSummarizingRelay) translateEvent(rawEvent json.RawMessage, fields map[string]interface{}) (ld.Event, error) {
	switch fields["kind"] {
	case ld.FeatureRequestEventKind:
		var e ld.FeatureRequestEvent
		err := json.Unmarshal(rawEvent, &e)
		if err != nil {
			return nil, err
		}
		// Look up the feature flag so we can set the event's TrackEvent and DebugEventsUntilDate properties,
		// and determine which variation index corresponds to the Value. If it's an unknown flag, then
		// Version and Variation are both omitted.
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
				for i, value := range flag.Variations {
					if reflect.DeepEqual(value, e.Value) {
						e.Variation = &i
						break
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
