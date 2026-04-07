package metrics

import (
	"encoding/json"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/ld-relay/v9/internal/events"
)

const relayUsageKind = "relayUsage"

// The relay will periodically capture usage information and emit that
// information through the event stream.
type relayUsageEvent struct {
	Kind             string `json:"kind"`
	RelayID          string `json:"relayId"`
	UserAgent        string `json:"userAgent"`
	PlatformCategory string `json:"platformCategory"`
	InstanceID       string `json:"instanceId,omitempty"`
	TagsHeader       string `json:"tagsHeader,omitempty"`

	FirstActive   ldtime.UnixMillisecondTime `json:"firstActive"`
	LastActive    ldtime.UnixMillisecondTime `json:"lastActive"`
	TotalStreamMs int64                      `json:"totalStreamMs"`
}

type usageKeyType struct {
	userAgent        string
	platformCategory string
	instanceID       string
	tagsHeader       string
}

type UsageActivityKind string

const (
	UsageActivityKindCount              UsageActivityKind = "count"
	UsageActivityKindStreamConnected    UsageActivityKind = "stream_connected"
	UsageActivityKindStreamDisconnected UsageActivityKind = "stream_disconnected"
)

type usageActivityMessage struct {
	envName string

	kind             UsageActivityKind
	userAgent        string
	platformCategory string
	instanceID       string
	tagsHeader       string
}

type (
	usageActivityFlush    struct{}
	usageActivityShutdown struct{}
)

// metricUsage is used to track usage information for a single
// user+platform+instance combination.
//
// This metric usage is updated when it receives count or streaming messages
// from the usage middleware. We then track that usage and report it upstream
// through our event processors.
type metricUsage struct {
	// The first and last active fields represent the period of activity for
	// this reporting usage.
	firstActive time.Time
	lastActive  time.Time

	// During the above duration, we need to report the total time spent
	// streaming from all connections.
	//
	// One option requires tracking the starting time for each connection. On
	// disconnect, we would add up the individual durations. This is
	// inefficient, as the relay may support 100k connections.
	//
	// Tracking the number of connections is a simpler solution, since the
	// calculation becomes:
	//
	//  elapsedStreaming = (lastActive - firstActive) * number of connections
	//
	// However, this isn't as accurate, since we need to account for:
	//
	// 1. A connection that starts after the first active time.
	// 2. A connection that disconnects before the last active time.
	//
	// To handle #1, when a new stream connects, we need to account for the
	// offset from the first active time.
	//
	//     0        10                                                60     adj
	//     |........[-------------------------------------------------|      -10
	//
	//     In the above example, the first active time is 0. A stream connects
	//     at 10, incrementing the streaming count to 1. We capture the offset
	//     (10-0=10) and store that in the streamingDurationAdjustment as a
	//     negative amount.
	//
	//     The total streaming duration calculation is then: 1 * (60 - 0) - 10 = 50
	//
	// To handle #2, when a stream disconnects, we need to calculate how long
	// it was connected, and add that to the running offset.
	//
	//     0        10      15                                        60     adj
	//     |.........[-------]........................................|      -10/+15
	//
	//     In the above example, the first active time is 0. A stream connects
	//     at 10, incrementing the streaming count to 1. We capture the offset (as outlined above),
	//	   and store that in the streamingDurationAdjustment as a negative amount.
	//
	//     The stream disconnects at 15, decrementing the streaming count to 0.
	//     We need to save the total stream duration using the point of
	//     disconnect and the first active time. We store that in the running duration
	//     adjustment as a positive amount.
	//
	//     The total streaming duration calculation is then: 0 * (60 - 0) - 5 = 5
	//
	// At the risk of being excessive, here is a final, more complicated example.
	//
	//     0        10     15          30                             60     adj.
	//     [---------]................................................|      +10
	//     [----------------------------------------------------------]      +0
	//     |................[-----------].............................]      -15/+30
	//     |................[-----------------------------------------]      -15
	//
	//     The total streaming duration calculation is then: 2 * (60 - 0) + 10 = 130
	//
	streamingCount              int64
	streamingDurationAdjustment time.Duration
}

// environmentMetricUsage is used to track usage information for a single
// environment. It tracks the first and last time a user+platform+instance
// combination was active, as well as the total streaming time consumed during
// that interval.
type environmentMetricUsage struct {
	relayID       string
	publisher     events.EventPublisher
	flushInterval time.Duration
	usageCh       chan interface{}

	// All of this data is expected to only be accessed from within a single go
	// routine
	usages map[usageKeyType]*metricUsage
}

func NewEnvironmentMetricUsage(relayID string, publisher events.EventPublisher, flushInterval time.Duration) *environmentMetricUsage {
	e := &environmentMetricUsage{
		relayID:       relayID,
		publisher:     publisher,
		flushInterval: flushInterval,
		usageCh:       make(chan interface{}),

		usages: make(map[usageKeyType]*metricUsage),
	}

	go e.run()

	return e
}

func (e *environmentMetricUsage) usageActivityMessage(usage *usageActivityMessage) {
	e.usageCh <- usage
}

func (e *environmentMetricUsage) run() {
	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			e.flushInternal()
		case usage, ok := <-e.usageCh:
			if !ok {
				return
			}
			now := time.Now()

			switch u := usage.(type) {
			case *usageActivityShutdown:
				e.flushInternal()
				return
			case *usageActivityFlush:
				e.flushInternal()
			case *usageActivityMessage:
				key := usageKeyType{userAgent: u.userAgent, platformCategory: u.platformCategory, instanceID: u.instanceID, tagsHeader: u.tagsHeader}
				if e.publisher == nil {
					continue
				}

				switch u.kind {
				case UsageActivityKindCount:
					usage, exists := e.usages[key]
					if !exists {
						usage = &metricUsage{
							firstActive: now,
						}
					}
					usage.lastActive = now
					e.usages[key] = usage
				case UsageActivityKindStreamConnected:
					usage, exists := e.usages[key]
					if !exists {
						usage = &metricUsage{
							firstActive: now,
						}
					}
					usage.lastActive = now
					usage.streamingCount += 1
					// Record how far into the metric interval we are so we can
					// calculate the total stream time later. See #1 in the
					// metricUsage comment above.
					usage.streamingDurationAdjustment -= now.Sub(usage.firstActive)

					e.usages[key] = usage
				case UsageActivityKindStreamDisconnected:
					usage, exists := e.usages[key]
					if !exists {
						usage = &metricUsage{
							firstActive: now,
						}
					} else {
						usage.streamingCount -= 1
					}

					usage.lastActive = now
					// When we disconnect, we add up the running time from the
					// earliest bit of activity. Because we already stored an
					// offset above, this should deal with any partial starts.
					// See #2 in the metricUsage comment above.
					usage.streamingDurationAdjustment += now.Sub(usage.firstActive)

					e.usages[key] = usage
				}
			}
		}
	}
}

func (e *environmentMetricUsage) flush() { //nolint:unused // used only in tests
	e.usageCh <- &usageActivityFlush{}
}

func (e *environmentMetricUsage) close() {
	e.usageCh <- &usageActivityShutdown{}
	close(e.usageCh)
}

func (e *environmentMetricUsage) flushInternal() {
	if e.publisher == nil {
		return
	}

	if len(e.usages) == 0 {
		return
	}

	now := time.Now()

	for key, usage := range e.usages {
		// Refer back to the metricUsage comment for an explanation on this
		// calculation.
		elapsedStreaming := now.Sub(usage.firstActive) * time.Duration(usage.streamingCount)
		lastActive := usage.lastActive

		// If we still have connected streaming clients, we need to make sure
		// the last active is up to the current point in time.
		if usage.streamingCount > 0 && lastActive.Before(now) {
			lastActive = now
		}

		relayUsageEvent := &relayUsageEvent{
			Kind:             relayUsageKind,
			RelayID:          e.relayID,
			UserAgent:        key.userAgent,
			PlatformCategory: key.platformCategory,
			InstanceID:       key.instanceID,
			TagsHeader:       key.tagsHeader,
			FirstActive:      ldtime.UnixMillisFromTime(usage.firstActive),
			LastActive:       ldtime.UnixMillisFromTime(lastActive),
			TotalStreamMs:    (elapsedStreaming + usage.streamingDurationAdjustment).Milliseconds(),
		}

		json, _ := json.Marshal(relayUsageEvent)
		e.publisher.Publish(events.EventPayloadMetadata{}, json)

		if usage.streamingCount == 0 {
			// If nothing is currently connected, then we should destroy the usage
			// completely.
			delete(e.usages, key)
		} else {
			// If we are still connected, then we are currently active.
			// However, no running totals or offsets should be calculated at
			// this point since all connections have been around since the new
			// event started.
			usage.firstActive = now
			usage.lastActive = now
			usage.streamingDurationAdjustment = 0
		}
	}
}
