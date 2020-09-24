package ldservices

import "github.com/launchdarkly/eventsource"

type testSSEEvent struct {
	id, event, data string
}

func (e testSSEEvent) Id() string    { return e.id } //nolint // standard capitalization would be ID(), but we didn't define this interface
func (e testSSEEvent) Event() string { return e.event }
func (e testSSEEvent) Data() string  { return e.data }

// NewSSEEvent constructs an implementation of eventsource.Event, to be used in testing eventsource or with
// StreamingServiceHandler.
func NewSSEEvent(id, event, data string) eventsource.Event {
	return testSSEEvent{id, event, data}
}
