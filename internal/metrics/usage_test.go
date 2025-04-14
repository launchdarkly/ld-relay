package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIndividualCountMessage(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	env.close()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.Equal(t, event.FirstActive, event.LastActive)
	assert.Equal(t, int64(0), event.TotalStreamMs)
}

func TestCountsWithDelay(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	time.Sleep(100 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.NotEqual(t, event.FirstActive, event.LastActive) // Ensure timestamps differ
}

func TestCountsWithDifferentKeyValues(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent1",
		platformCategory: "platform1",
		instanceID:       "instanceID1",
	})
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent2",
		platformCategory: "platform1",
		instanceID:       "instanceID1",
	})

	env.flush()

	firstEvent := publisher.expectUsageEvent(t, time.Second)
	secondEvent := publisher.expectUsageEvent(t, time.Second)
	if firstEvent.UserAgent != "userAgent1" {
		firstEvent, secondEvent = secondEvent, firstEvent
	}

	assert.Equal(t, "userAgent1", firstEvent.UserAgent)
	assert.Equal(t, "platform1", firstEvent.PlatformCategory)
	assert.Equal(t, "instanceID1", firstEvent.InstanceID)
	assert.Equal(t, firstEvent.FirstActive, firstEvent.LastActive)
	assert.Equal(t, int64(0), firstEvent.TotalStreamMs)

	assert.Equal(t, "userAgent2", secondEvent.UserAgent)
	assert.Equal(t, "platform1", secondEvent.PlatformCategory)
	assert.Equal(t, "instanceID1", secondEvent.InstanceID)
	assert.Equal(t, secondEvent.FirstActive, secondEvent.LastActive)
	assert.Equal(t, int64(0), secondEvent.TotalStreamMs)
}

func TestFlushesPeriodically(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	time.Sleep(10 * time.Millisecond) // Ensure the flush interval has passed

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.Equal(t, int64(0), event.TotalStreamMs)
}

func TestNoActivityYieldsNoEvents(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	// No activity, just attempt to flush
	env.flush()
	publisher.expectNoUsageEvent(t, time.Second)
}

func TestFlushingResetsCounts(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()
	publisher.expectUsageEvent(t, time.Second)

	env.flush()
	publisher.expectNoUsageEvent(t, time.Second)
}

func TestCapturesSimpleStreamDuration(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	time.Sleep(100 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.InDeltaf(t, 100, event.TotalStreamMs, 50, "stream time should be approximately 100ms")
}

func TestStreamWithoutDisconnect(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	// Connect to stream but don't disconnect
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	time.Sleep(100 * time.Millisecond)

	// Flush without disconnecting
	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.InDeltaf(t, 100, event.TotalStreamMs, 50, "stream time should be approximately 100ms")

	// Stream is still connected, so wait and try to force another event.
	time.Sleep(200 * time.Millisecond)
	env.flush()

	event = publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.InDeltaf(t, 200, event.TotalStreamMs, 50, "stream time should be approximately 200ms")
}

func TestStreamDurationIsNotAffectedByActivityCounts(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	time.Sleep(200 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	time.Sleep(100 * time.Millisecond)

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.InDeltaf(t, 100, event.TotalStreamMs, 50, "stream time should be approximately 100ms")
}

func TestNonoverlappingStreams(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	// First stream session: ~100ms
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	time.Sleep(100 * time.Millisecond)
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	// Second stream session: ~200ms
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	time.Sleep(200 * time.Millisecond)
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.InDeltaf(t, 300, event.TotalStreamMs, 50, "stream time should be approximately 300ms")
}

func TestOverlappingStreams(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	time.Sleep(100 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	time.Sleep(100 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)

	assert.InDeltaf(t, 300, event.TotalStreamMs, 50, "stream time should be approximately 300ms")
}

func TestMultipleUserStreams(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	// First user connects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent1",
		platformCategory: "platform1",
		instanceID:       "instanceID1",
	})

	time.Sleep(50 * time.Millisecond)

	// Second user connects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent2",
		platformCategory: "platform2",
		instanceID:       "instanceID2",
	})

	time.Sleep(100 * time.Millisecond)

	// First user disconnects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent1",
		platformCategory: "platform1",
		instanceID:       "instanceID1",
	})

	time.Sleep(50 * time.Millisecond)

	// Second user disconnects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent2",
		platformCategory: "platform2",
		instanceID:       "instanceID2",
	})

	env.flush()

	// Check first user's event
	event1 := publisher.expectUsageEvent(t, time.Second)
	event2 := publisher.expectUsageEvent(t, time.Second)

	if event1.UserAgent != "userAgent1" {
		event1, event2 = event2, event1
	}

	assert.Equal(t, "platform1", event1.PlatformCategory)
	assert.Equal(t, "instanceID1", event1.InstanceID)
	assert.InDeltaf(t, 150, event1.TotalStreamMs, 50, "stream time should be approximately 150ms")

	assert.Equal(t, "userAgent2", event2.UserAgent)
	assert.Equal(t, "platform2", event2.PlatformCategory)
	assert.Equal(t, "instanceID2", event2.InstanceID)
	assert.InDeltaf(t, 150, event2.TotalStreamMs, 50, "stream time should be approximately 150ms")
}
