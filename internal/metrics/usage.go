package metrics

import (
	"encoding/json"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
)

// The relay will periodically capture usage information and emit that
// information through the event stream.
type relayUsageEvent struct {
	RelayID          string `json:"relayId"`
	UserAgent        string `json:"userAgent"`
	PlatformCategory string `json:"platformCategory"`
	InstanceID       string `json:"instanceId,omitempty"`

	FirstActive   ldtime.UnixMillisecondTime `json:"firstActive"`
	LastActive    ldtime.UnixMillisecondTime `json:"lastActive"`
	TotalStreamMs int64                      `json:"totalStreamMs"`
}

type usageKeyType struct {
	userAgent        string
	platformCategory string
	instanceID       string
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
}

type usageActivityFlush struct{}
type usageActivityShutdown struct{}

// metricUsage is used to track usage information for a single
// user+platform+instance combination.
//
// This metric usage is updated when it receives count or streaming messages
// from the usage middleware. We then track that usage and report it upstream
// through our event processors.
type metricUsage struct {
	firstActive time.Time
	lastActive  time.Time

	streamingCount      int64
	streamingOffset     time.Duration
	streamingRunningSum time.Duration
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
				key := usageKeyType{userAgent: u.userAgent, platformCategory: u.platformCategory, instanceID: u.instanceID}
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
					// flushInterval comment below.
					usage.streamingOffset += now.Sub(usage.firstActive)

					e.usages[key] = usage
				case UsageActivityKindStreamDisconnected:
					usage, exists := e.usages[key]
					if !exists {
						continue
					}
					usage.lastActive = now
					usage.streamingCount -= 1
					// When we disconnect, we add up the running time from the
					// earliest bit of activity. Because we already stored an
					// offset above, this should deal with any partial starts.
					// See #2 in the flushInterval comment below.
					usage.streamingRunningSum += now.Sub(usage.firstActive)

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
		// We calculate the elapsedStreaming time to be N*the duration of this event.
		// However, we have to deal with two situations:
		// 1. A streaming connection starts after we have already seen usage for that instance.
		// 2. A streaming connection disconnects before we have reported the usage for that instance.
		//
		// We handle #1 by removing the running offset we have been calculating when streams connect.
		// We handle #2 by adding the running sum we have been calculating when streams disconnect.
		elapsedStreaming := now.Sub(usage.firstActive) * time.Duration(usage.streamingCount)

		relayUsageEvent := &relayUsageEvent{
			RelayID:          e.relayID,
			UserAgent:        key.userAgent,
			PlatformCategory: key.platformCategory,
			InstanceID:       key.instanceID,
			FirstActive:      ldtime.UnixMillisFromTime(usage.firstActive),
			LastActive:       ldtime.UnixMillisFromTime(usage.lastActive),
			TotalStreamMs:    (usage.streamingRunningSum + elapsedStreaming - usage.streamingOffset).Milliseconds(),
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
			usage.streamingOffset = 0
			usage.streamingRunningSum = 0
		}
	}
}
