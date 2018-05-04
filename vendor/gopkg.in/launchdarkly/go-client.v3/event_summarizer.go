package ldclient

// Manages the state of summarizable information for the EventProcessor, including the
// event counters and user deduplication. Note that the methods for this type are
// deliberately not thread-safe, because they should always be called from EventProcessor's
// single event-processing goroutine.
type eventSummarizer struct {
	eventsState eventSummary
}

type eventSummary struct {
	counters  map[counterKey]*counterValue
	startDate uint64
	endDate   uint64
}

type counterKey struct {
	key       string
	variation int
	version   int
}

const (
	nilVariation = -1
)

type counterValue struct {
	count       int
	flagValue   interface{}
	flagDefault interface{}
}

func newEventSummarizer() eventSummarizer {
	return eventSummarizer{eventsState: newEventSummary()}
}

func newEventSummary() eventSummary {
	return eventSummary{
		counters: make(map[counterKey]*counterValue),
	}
}

// Adds this event to our counters, if it is a type of event we need to count.
func (s *eventSummarizer) summarizeEvent(evt Event) {
	var fe FeatureRequestEvent
	var ok bool
	if fe, ok = evt.(FeatureRequestEvent); !ok {
		return
	}

	key := counterKey{key: fe.Key}
	if fe.Variation != nil {
		key.variation = *fe.Variation
	} else {
		key.variation = nilVariation
	}
	if fe.Version != nil {
		key.version = *fe.Version
	}

	if value, ok := s.eventsState.counters[key]; ok {
		value.count++
	} else {
		s.eventsState.counters[key] = &counterValue{
			count:       1,
			flagValue:   fe.Value,
			flagDefault: fe.Default,
		}
	}

	creationDate := fe.CreationDate
	if s.eventsState.startDate == 0 || creationDate < s.eventsState.startDate {
		s.eventsState.startDate = creationDate
	}
	if creationDate > s.eventsState.endDate {
		s.eventsState.endDate = creationDate
	}
}

// Returns a snapshot of the current summarized event data.
func (s *eventSummarizer) snapshot() eventSummary {
	return s.eventsState
}

func (s *eventSummarizer) reset() {
	s.eventsState = newEventSummary()
}
