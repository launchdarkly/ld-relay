package ldcomponents

import (
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

type nullEventProcessorFactory struct{}

// NoEvents returns a configuration object that disables analytics events.
//
// Storing this in Config.Events causes the SDK to discard all analytics events and not send them to
// LaunchDarkly, regardless of any other configuration.
//
//     config := ld.Config{
//         Events: ldcomponents.NoEvents(),
//     }
func NoEvents() interfaces.EventProcessorFactory {
	return nullEventProcessorFactory{}
}

func (f nullEventProcessorFactory) CreateEventProcessor(
	context interfaces.ClientContext,
) (ldevents.EventProcessor, error) {
	return ldevents.NewNullEventProcessor(), nil
}

// This method implements a hidden interface in ldclient_events.go, as a hint to the SDK that this is
// the stub implementation of EventProcessorFactory and therefore LDClient does not need to bother
// generating events at all.
func (f nullEventProcessorFactory) IsNullEventProcessorFactory() bool {
	return true
}
