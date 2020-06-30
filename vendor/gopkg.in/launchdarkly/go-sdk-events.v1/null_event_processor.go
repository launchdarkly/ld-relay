package ldevents

type nullEventProcessor struct{}

// NewNullEventProcessor creates a no-op implementation of EventProcessor.
func NewNullEventProcessor() EventProcessor {
	return nullEventProcessor{}
}

func (n nullEventProcessor) SendEvent(e Event) {}

func (n nullEventProcessor) Flush() {}

func (n nullEventProcessor) Close() error {
	return nil
}
