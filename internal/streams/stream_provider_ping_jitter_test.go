package streams

import (
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPingStreamJitterDelaysPings verifies that when jitter is configured,
// ping events are delayed by a random duration between [jitterTime/2, jitterTime].
func TestPingStreamJitterDelaysPings(t *testing.T) {
	validCredential := sdkauth.New(testMobileKey)
	jitterTime := 200 * time.Millisecond

	sp := NewStreamProvider(basictypes.MobilePingStream, 0, jitterTime)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore(nil, nil)
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Consume initial ping event
		expectEvent(t, eventCh, MakePingEvent())

		// Trigger an update and measure how long it takes to receive the ping
		start := time.Now()
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))

		// Should receive the ping after a delay
		event := helpers.RequireValue(t, eventCh, jitterTime*2, "timed out waiting for delayed ping")
		elapsed := time.Since(start)

		require.NotNil(t, event)
		assert.Equal(t, MakePingEvent().Event(), event.Event())

		// Verify the delay is within the expected range [jitterTime/2, jitterTime]
		minDelay := jitterTime / 2
		maxDelay := jitterTime + 50*time.Millisecond // Add buffer for timing variance

		assert.GreaterOrEqual(t, elapsed, minDelay,
			"ping should be delayed by at least %v, but was delayed by %v", minDelay, elapsed)
		assert.LessOrEqual(t, elapsed, maxDelay,
			"ping should be delayed by at most %v, but was delayed by %v", jitterTime, elapsed)
	})
}

// TestPingStreamJitterCoalescesMultiplePings verifies that multiple ping events
// received during the jitter delay period are coalesced into a single ping.
func TestPingStreamJitterCoalescesMultiplePings(t *testing.T) {
	validCredential := sdkauth.New(testMobileKey)
	jitterTime := 200 * time.Millisecond

	sp := NewStreamProvider(basictypes.MobilePingStream, 0, jitterTime)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore(nil, nil)
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Consume initial ping event
		expectEvent(t, eventCh, MakePingEvent())

		// Send multiple updates in quick succession
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
		time.Sleep(10 * time.Millisecond)
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag2.Key, sharedtest.FlagDesc(testFlag2))
		time.Sleep(10 * time.Millisecond)
		esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))

		// Should receive only ONE ping event after the jitter delay
		event := helpers.RequireValue(t, eventCh, jitterTime*2, "timed out waiting for coalesced ping")
		require.NotNil(t, event)
		assert.Equal(t, MakePingEvent().Event(), event.Event())

		// Verify no additional pings are sent
		expectNoEvent(t, eventCh)
	})
}

// TestPingStreamNoJitterSendsPingsImmediately verifies that when jitter is 0,
// ping events are sent immediately without delay.
func TestPingStreamNoJitterSendsPingsImmediately(t *testing.T) {
	validCredential := sdkauth.New(testMobileKey)

	sp := NewStreamProvider(basictypes.MobilePingStream, 0, 0)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore(nil, nil)
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Consume initial ping event
		expectEvent(t, eventCh, MakePingEvent())

		// Send an update and verify it's received immediately (within a reasonable time)
		start := time.Now()
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))

		event := helpers.RequireValue(t, eventCh, 100*time.Millisecond, "timed out waiting for immediate ping")
		elapsed := time.Since(start)

		require.NotNil(t, event)
		assert.Equal(t, MakePingEvent().Event(), event.Event())

		// Verify the ping was sent quickly (less than 50ms overhead)
		assert.Less(t, elapsed, 50*time.Millisecond,
			"ping without jitter should be sent immediately, but took %v", elapsed)
	})
}

// TestPingStreamNoJitterSendsMultiplePings verifies that when jitter is 0,
// multiple updates result in multiple ping events.
func TestPingStreamNoJitterSendsMultiplePings(t *testing.T) {
	validCredential := sdkauth.New(testMobileKey)

	sp := NewStreamProvider(basictypes.MobilePingStream, 0, 0)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore(nil, nil)
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Consume initial ping event
		expectEvent(t, eventCh, MakePingEvent())

		// Send multiple updates
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
		expectEvent(t, eventCh, MakePingEvent())

		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag2.Key, sharedtest.FlagDesc(testFlag2))
		expectEvent(t, eventCh, MakePingEvent())

		esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))
		expectEvent(t, eventCh, MakePingEvent())
	})
}

// TestJSClientPingStreamJitter verifies that jitter works for JS client ping streams.
func TestJSClientPingStreamJitter(t *testing.T) {
	validCredential := sdkauth.New(testEnvID)
	jitterTime := 200 * time.Millisecond

	sp := NewStreamProvider(basictypes.JSClientPingStream, 0, jitterTime)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore(nil, nil)
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Consume initial ping event
		expectEvent(t, eventCh, MakePingEvent())

		// Send multiple updates in quick succession
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
		time.Sleep(10 * time.Millisecond)
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag2.Key, sharedtest.FlagDesc(testFlag2))

		// Should receive only ONE ping event after the jitter delay
		event := helpers.RequireValue(t, eventCh, jitterTime*2, "timed out waiting for coalesced ping")
		require.NotNil(t, event)
		assert.Equal(t, MakePingEvent().Event(), event.Event())

		// Verify no additional pings are sent
		expectNoEvent(t, eventCh)
	})
}

// TestServerSideStreamNoJitter verifies that server-side streams don't use jitter
// and continue to send all flag data immediately.
func TestServerSideStreamNoJitter(t *testing.T) {
	validCredential := sdkauth.New(testSDKKey)

	// Server-side streams are created with jitter=0 regardless of config
	sp := NewStreamProvider(basictypes.ServerSideStream, 0, 100*time.Millisecond)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore([]ldmodel.FeatureFlag{testFlag1}, []ldmodel.Segment{testSegment1})
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Server-side streams send a "put" event with all data immediately
		event := helpers.RequireValue(t, eventCh, 100*time.Millisecond, "timed out waiting for put event")
		require.NotNil(t, event)
		assert.Equal(t, "put", event.Event())

		// Send an update - should receive a "patch" event immediately
		start := time.Now()
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag2.Key, sharedtest.FlagDesc(testFlag2))

		event = helpers.RequireValue(t, eventCh, 100*time.Millisecond, "timed out waiting for patch event")
		elapsed := time.Since(start)

		require.NotNil(t, event)
		assert.Equal(t, "patch", event.Event())

		// Verify the event was sent immediately (no jitter applied)
		assert.Less(t, elapsed, 50*time.Millisecond,
			"server-side stream should send events immediately, but took %v", elapsed)
	})
}

// TestPingStreamJitterSubsequentUpdatesAfterDelay verifies that after a jittered
// ping is sent, subsequent updates trigger a new jittered delay.
func TestPingStreamJitterSubsequentUpdatesAfterDelay(t *testing.T) {
	validCredential := sdkauth.New(testMobileKey)
	jitterTime := 150 * time.Millisecond

	sp := NewStreamProvider(basictypes.MobilePingStream, 0, jitterTime)
	require.NotNil(t, sp)
	defer sp.Close()

	store := makeMockStore(nil, nil)
	esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
	require.NotNil(t, esp)
	defer esp.Close()

	handler := sp.Handler(validCredential)
	require.NotNil(t, handler)

	req, _ := http.NewRequest("GET", "", nil)
	sharedtest.WithStreamRequest(t, req, handler, func(eventCh <-chan eventsource.Event) {
		// Consume initial ping event
		expectEvent(t, eventCh, MakePingEvent())

		// First update - should be jittered
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
		event1 := helpers.RequireValue(t, eventCh, jitterTime*2, "timed out waiting for first ping")
		require.NotNil(t, event1)

		// Wait for jitter to complete
		time.Sleep(jitterTime + 50*time.Millisecond)

		// Second update - should also be jittered
		start := time.Now()
		esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag2.Key, sharedtest.FlagDesc(testFlag2))
		event2 := helpers.RequireValue(t, eventCh, jitterTime*2, "timed out waiting for second ping")
		elapsed := time.Since(start)

		require.NotNil(t, event2)

		// Verify the second ping was also delayed
		minDelay := jitterTime / 2
		assert.GreaterOrEqual(t, elapsed, minDelay,
			"second ping should also be jittered, but was delayed by only %v", elapsed)
	})
}
