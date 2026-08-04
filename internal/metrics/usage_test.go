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
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	clk.advance(1 * time.Millisecond)

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
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	clk.advance(10 * time.Millisecond)

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
	assert.Equal(t, int64(10), event.TotalStreamMs)
}

func TestStreamWithoutDisconnect(t *testing.T) {
	publisher := newTestEventsPublisher()
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	// Connect to stream but don't disconnect
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	clk.advance(10 * time.Millisecond)

	// Flush without disconnecting
	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.NotEqual(t, event.FirstActive, event.LastActive) // Ensure timestamps differ
	assert.Equal(t, int64(10), event.TotalStreamMs)

	// Stream is still connected, so let more time pass and force another event.
	clk.advance(30 * time.Millisecond)
	env.flush()

	event = publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.NotEqual(t, event.FirstActive, event.LastActive) // Ensure timestamps differ
	assert.Equal(t, int64(30), event.TotalStreamMs)
}

func TestStreamDisconnectWithoutConnect(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	// Connect to stream but don't disconnect
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	// Flush without disconnecting
	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, int64(0), event.TotalStreamMs)
	assert.Equal(t, event.FirstActive, event.LastActive)
}

func TestStreamDurationIsNotAffectedByActivityCounts(t *testing.T) {
	publisher := newTestEventsPublisher()
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	clk.advance(20 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	clk.advance(10 * time.Millisecond)

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.Equal(t, int64(10), event.TotalStreamMs)
}

func TestNonoverlappingStreams(t *testing.T) {
	publisher := newTestEventsPublisher()
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	// First stream session: 10ms
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	clk.advance(10 * time.Millisecond)
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	// Second stream session: 30ms
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})
	clk.advance(30 * time.Millisecond)
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, int64(40), event.TotalStreamMs)
}

func TestOverlappingStreams(t *testing.T) {
	publisher := newTestEventsPublisher()
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	clk.advance(100 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	clk.advance(100 * time.Millisecond)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
	})

	env.flush()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)

	// First connection streams for the full 200ms; the second overlaps for 100ms.
	assert.Equal(t, int64(300), event.TotalStreamMs)
}

func TestMultipleUserStreams(t *testing.T) {
	publisher := newTestEventsPublisher()
	clk := newFakeClock()
	env := newEnvironmentMetricUsage("relayID", publisher, 1*time.Hour, clk.now)

	// First user connects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent1",
		platformCategory: "platform1",
		instanceID:       "instanceID1",
	})

	clk.advance(50 * time.Millisecond)

	// Second user connects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamConnected,
		userAgent:        "userAgent2",
		platformCategory: "platform2",
		instanceID:       "instanceID2",
	})

	clk.advance(100 * time.Millisecond)

	// First user disconnects
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindStreamDisconnected,
		userAgent:        "userAgent1",
		platformCategory: "platform1",
		instanceID:       "instanceID1",
	})

	clk.advance(200 * time.Millisecond)

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
	assert.Equal(t, int64(150), event1.TotalStreamMs)

	assert.Equal(t, "userAgent2", event2.UserAgent)
	assert.Equal(t, "platform2", event2.PlatformCategory)
	assert.Equal(t, "instanceID2", event2.InstanceID)
	assert.Equal(t, int64(300), event2.TotalStreamMs)
}

func TestTagsHeaderIsIncludedInUsageEvent(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
		tagsHeader:       "application-id/my-app application-version/1.0",
	})
	env.close()

	event := publisher.expectUsageEvent(t, time.Second)
	assert.Equal(t, "userAgent", event.UserAgent)
	assert.Equal(t, "platform", event.PlatformCategory)
	assert.Equal(t, "instanceID", event.InstanceID)
	assert.Equal(t, "application-id/my-app application-version/1.0", event.TagsHeader)
}

func TestDifferentTagsHeadersProduceSeparateEvents(t *testing.T) {
	publisher := newTestEventsPublisher()
	env := NewEnvironmentMetricUsage("relayID", publisher, 1*time.Hour)

	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
		tagsHeader:       "application-id/app1",
	})
	env.usageActivityMessage(&usageActivityMessage{
		kind:             UsageActivityKindCount,
		userAgent:        "userAgent",
		platformCategory: "platform",
		instanceID:       "instanceID",
		tagsHeader:       "application-id/app2",
	})

	env.flush()

	firstEvent := publisher.expectUsageEvent(t, time.Second)
	secondEvent := publisher.expectUsageEvent(t, time.Second)
	if firstEvent.TagsHeader != "application-id/app1" {
		firstEvent, secondEvent = secondEvent, firstEvent
	}

	assert.Equal(t, "application-id/app1", firstEvent.TagsHeader)
	assert.Equal(t, "application-id/app2", secondEvent.TagsHeader)
}
