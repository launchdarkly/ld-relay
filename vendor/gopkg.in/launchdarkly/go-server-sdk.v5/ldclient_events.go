package ldclient

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	ldeval "gopkg.in/launchdarkly/go-server-sdk-evaluation.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

// This file contains internal support code for LDClient's interactions with the analytics event pipeline.
//
// General implementation notes:
//
// Under normal circumstances, an analytics event is generated whenever 1. a flag is evaluated explicitly
// with a Variation method, 2. a flag is evaluated indirectly as a prerequisite, or 3. the application
// explicitly generates an event by calling Identify or Track. This event is submitted to the configured
// EventProcessor's SendEvent method; the EventProcessor then does any necessary processing and eventually
// delivers the event data to LaunchDarkly, either as a full event or in summary data. The implementation
// of that logic is all in go-sdk-events (since it can be used outside of the SDK, as in ld-relay).
//
// In the current implementation, these event objects are structs which are then cast to the common Event
// interface, which necessarily means they are allocated on the heap. If events are completely disabled
// (config.Events = ldcomponents.NoEvents()), we can avoid this overhead by not creating these ephemeral
// objects at all. Even though NoEvents is just another implementation of EventProcessorFactory, we can
// detect its special nature using the hidden interface method "IsNullEventProcessorFactory".

type nullEventProcessorFactoryDescription interface {
	IsNullEventProcessorFactory() bool
}

func isNullEventProcessorFactory(f interfaces.EventProcessorFactory) bool {
	if nf, ok := f.(nullEventProcessorFactoryDescription); ok {
		return nf.IsNullEventProcessorFactory()
	}
	return false
}

func getEventProcessorFactory(config Config) interfaces.EventProcessorFactory {
	if config.Offline {
		return ldcomponents.NoEvents()
	}
	if config.Events == nil {
		return ldcomponents.SendEvents()
	}
	return config.Events
}

// This struct is used during evaluations to keep track of the event generation strategy we are using
// (with or without evaluation reasons). It captures all of the relevant state so that we do not need to
// create any more stateful objects, such as closures, to generate events during an evaluation. See
// CONTRIBUTING.md for performance issues with closures.
type eventsScope struct {
	disabled                  bool
	factory                   ldevents.EventFactory
	prerequisiteEventRecorder ldeval.PrerequisiteFlagEventRecorder
}

func newDisabledEventsScope() eventsScope {
	return eventsScope{disabled: true}
}

func newEventsScope(client *LDClient, withReasons bool) eventsScope {
	factory := ldevents.NewEventFactory(withReasons, nil)
	return eventsScope{
		factory: factory,
		prerequisiteEventRecorder: func(params ldeval.PrerequisiteFlagEvent) {
			event := factory.NewSuccessfulEvalEvent(
				params.PrerequisiteFlag,
				ldevents.User(params.User),
				params.PrerequisiteResult.VariationIndex,
				params.PrerequisiteResult.Value,
				ldvalue.Null(),
				params.PrerequisiteResult.Reason,
				params.TargetFlagKey,
			)
			client.eventProcessor.SendEvent(event)
		},
	}
}
