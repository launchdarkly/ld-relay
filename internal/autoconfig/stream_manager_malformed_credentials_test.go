package autoconfig

// Tests for a payload that parses as JSON but whose credentials cannot produce a usable accepted set.
// The environment keeps the credentials it already had, and the cache keeps its last-good entry.
//
// Whether the stream reconnects depends on how much of the event was usable. An event relay could
// not use at all is asked for again. A put that applied at least one environment is not, because the
// refused environment would come back identical and the reconnect would interrupt every healthy
// environment in the meantime.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envWithUnusableCredentials returns a copy of env whose sdkKeys array does not contain the
// designated sdkKey, which BuildAcceptedSet refuses. The JSON is well formed, so this exercises the
// credential check rather than the parse.
func envWithUnusableCredentials(env envfactory.EnvironmentRep) envfactory.EnvironmentRep {
	env.SDKKeys = []envfactory.ConcurrentKeyRep{
		{Key: "some-other-key", Value: "a-key-that-is-not-the-designated-one"},
	}
	return env
}

func TestMalformedCredentialPayloadIsRefusedAndRestartsTheStream(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))

		// Nothing is handed to the relay: the environment keeps what it had.
		p.requireNoMoreMessages()

		// The stream reconnects, because the service has no way to learn that relay refused the
		// payload and would otherwise send nothing further.
		awaitStreamRequest(t, p)
	})
}

// TestReplayOfTheSameVersionIsAppliedAfterARefusal is the reason validation runs before Upsert.
//
// Upsert deduplicates by version. If a refused payload had already recorded its version, the replay
// that follows the reconnect would carry the same version and be dropped as a no-op, leaving the
// environment on credentials it should have replaced with no path back short of a restart.
func TestReplayOfTheSameVersionIsAppliedAfterARefusal(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		// Establish the environment, then refuse an update at version 11.
		p.stream.Enqueue(makeEnvPutEvent(testEnv1))
		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		p.requireReceivedAllMessage()

		refused := envWithUnusableCredentials(testEnv1)
		refused.Version = testEnv1.Version + 1
		p.stream.Enqueue(makePatchEnvEvent(refused))
		p.requireNoMoreMessages()

		// The service replays the same version, this time in a shape relay can use. It must be
		// applied rather than deduplicated away.
		replayed := testEnv1
		replayed.Version = testEnv1.Version + 1
		replayed.EnvName = "renamed-by-the-replay"
		p.stream.Enqueue(makePatchEnvEvent(replayed))

		msg = p.requireMessage()
		require.NotNil(t, msg.update, "the replay of a refused version must be applied")
		assert.Equal(t, "renamed-by-the-replay", msg.update.Identifiers.EnvName)
	})
}

func TestPutWithOneRefusedEnvironmentStillAppliesTheOthers(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))

		msg := p.requireMessage()
		require.NotNil(t, msg.add, "a well-formed environment in the same put must still be applied")
		assert.Equal(t, testEnv1.EnvID, msg.add.EnvID)
		p.requireReceivedAllMessage()
		p.requireNoMoreMessages()
	})
}

// recordingCache captures what the stream manager writes, and serves a fixed prior snapshot.
type recordingCache struct {
	mu       sync.Mutex
	previous *PutContent
	written  []PutContent
	getErr   error
}

func (c *recordingCache) GetAll(context.Context) (*PutContent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.previous, c.getErr
}

func (c *recordingCache) SetAll(_ context.Context, content PutContent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, content)
	return nil
}

func (c *recordingCache) Upsert(context.Context, CacheKind, string, interface{}) error { return nil }
func (c *recordingCache) Delete(context.Context, CacheKind, string) error              { return nil }
func (c *recordingCache) Close() error                                                 { return nil }

func (c *recordingCache) lastWrite() (PutContent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.written) == 0 {
		return PutContent{}, false
	}
	return c.written[len(c.written)-1], true
}

func TestCacheKeepsTheLastGoodEntryForARefusedEnvironment(t *testing.T) {
	// The cache already holds a good entry for env2, which the incoming put ruins.
	cache := &recordingCache{
		previous: &PutContent{Environments: map[config.EnvironmentID]envfactory.EnvironmentRep{
			testEnv2.EnvID: testEnv2,
		}},
	}

	streamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithCache(t, streamHandler, stream, cache, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))
		_ = p.requireMessage()
		p.requireReceivedAllMessage()

		require.Eventually(t, func() bool { _, ok := cache.lastWrite(); return ok },
			time.Second, time.Millisecond, "timed out waiting for the cache write")

		written, _ := cache.lastWrite()
		assert.Equal(t, testEnv1, written.Environments[testEnv1.EnvID],
			"the well-formed environment is written as received")
		assert.Equal(t, testEnv2, written.Environments[testEnv2.EnvID],
			"the refused environment keeps its last-good cached entry rather than being dropped")
	})
}

func TestCacheIsLeftAloneWhenThePriorSnapshotCannotBeRead(t *testing.T) {
	cache := &recordingCache{getErr: assert.AnError}

	streamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithCache(t, streamHandler, stream, cache, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))
		_ = p.requireMessage()
		p.requireReceivedAllMessage()

		// Rewriting the snapshot from partial data would lose the refused environment, so nothing is
		// written at all.
		assert.Never(t, func() bool { _, ok := cache.lastWrite(); return ok },
			300*time.Millisecond, 20*time.Millisecond)
	})
}

// awaitStreamRequest waits for the next request the stream manager makes, which is how a reconnect
// is observed.
func awaitStreamRequest(t *testing.T, p streamManagerTestParams) {
	t.Helper()
	select {
	case <-p.requestsCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a stream request")
	}
}

func TestRefusedCredentialPayloadRecordsAnInvalidDataError(t *testing.T) {
	// The refusal path shares its status reporting with a parse failure, but no test drove it through
	// a credential refusal, which is the case this change added. Without the status transition an
	// operator watching autoConfigStatus has no signal that relay is refusing what it is being sent.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))

		// Only the error is asserted. The refusal restarts the stream, so State races back to VALID
		// as soon as the new connection is up, and LastError is what survives for an operator to read.
		status := requireStatusEventually(t, p, "expected an invalid-data error", func(s StreamStatus) bool {
			return s.LastError.Kind == interfaces.DataSourceErrorKindInvalidData
		})
		assert.False(t, status.LastError.Time.IsZero(), "the error must carry when it happened")
	})
}

func TestOneRefusalReconnectsAtOnceAndASecondSlowsDown(t *testing.T) {
	// The threshold is the whole point of malformedBackoffThreshold: a single corrupt event is worth
	// asking about again immediately, and a second identical one means asking faster will not help.
	// Nothing asserted either half, so lowering the threshold to 1 or raising it left every test green.
	const extendedBase = 300 * time.Millisecond

	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = extendedBase
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))
		awaitStreamRequest(t, p)

		// The first refusal reconnects on the short curve.
		var afterFirst []time.Duration
		require.Eventually(t, func() bool {
			afterFirst = reconnectDelays(p.mockLog)
			return len(afterFirst) >= 1
		}, 2*time.Second, 10*time.Millisecond, "expected a logged reconnect delay")
		assert.Less(t, afterFirst[0], extendedBase/2,
			"the first unusable event must not engage the extended delays")

		// The second consecutive refusal does engage them.
		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))

		require.Eventually(t, func() bool {
			delays := reconnectDelays(p.mockLog)
			return len(delays) > len(afterFirst) && delays[len(delays)-1] >= extendedBase/2
		}, 5*time.Second, 10*time.Millisecond,
			"a second consecutive unusable event must move the stream to the extended delays")
	})
}

func TestAUsableEventReturnsTheStreamToTheShortRetryCurve(t *testing.T) {
	// eventsource clears an activated profile only after a stretch of healthy operation, and it
	// measures that stretch from a timestamp stamped as the event was delivered, so its own reset
	// never fires on an event-driven restart. Without an explicit revert the first pair of refused
	// payloads leaves the stream on the extended delays for the life of the process, and every later
	// reconnect waits minutes even though the stream has been healthy in between.
	const extendedBase = 300 * time.Millisecond

	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = extendedBase
		p.startStream()
		awaitStreamRequest(t, p)

		// Two refusals engage the extended profile.
		for i := 0; i < 2; i++ {
			p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))
			awaitStreamRequest(t, p)
		}
		require.Eventually(t, func() bool {
			delays := reconnectDelays(p.mockLog)
			return len(delays) > 0 && delays[len(delays)-1] >= extendedBase/2
		}, 5*time.Second, 10*time.Millisecond, "expected the extended delays to be engaged")

		// A usable event proves the stream works again.
		p.stream.Enqueue(makePatchEnvEvent(testEnv1))
		p.requireMessage()

		before := len(reconnectDelays(p.mockLog))

		// The next refusal is the first of a new run, so it restarts without asking for a backoff.
		// The delay it waits is the one under test.
		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv2)))

		var latest time.Duration
		require.Eventually(t, func() bool {
			delays := reconnectDelays(p.mockLog)
			if len(delays) <= before {
				return false
			}
			latest = delays[len(delays)-1]
			return true
		}, 5*time.Second, 10*time.Millisecond, "expected another reconnect delay")

		assert.Less(t, latest, extendedBase/2,
			"a recovered stream must reconnect on the short curve, not the extended one")
	})
}

func TestAPartialRefusalKeepsTheStreamConnected(t *testing.T) {
	// One environment's bad data must not take the connection down. The refused environment would
	// come back identical on a new connection, so a reconnect achieves nothing, and two consecutive
	// partial refusals used to walk the whole stream onto the extended delays. Every other
	// environment then waited up to an hour for its own updates because of one environment's data.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))
		msg := p.requireMessage()
		require.NotNil(t, msg.add, "the well-formed environment in the put must be applied")
		p.requireReceivedAllMessage()

		// A second consecutive partial refusal is what used to engage the extended delays. Bump the
		// good environment so it is a real update rather than a version the receiver dedupes away.
		updatedEnv1 := testEnv1
		updatedEnv1.Version++
		p.stream.Enqueue(makeEnvPutEvent(updatedEnv1, envWithUnusableCredentials(testEnv2)))
		msg = p.requireMessage()
		require.NotNil(t, msg.update, "the healthy environment must keep receiving updates")
		p.requireReceivedAllMessage()

		// The connection that delivered both puts is still the one in use.
		if !helpers.AssertNoMoreValues(t, p.requestsCh, 300*time.Millisecond,
			"the stream must not reconnect because one environment in a put was refused") {
			t.FailNow()
		}

		// The refusal is still reported, without claiming the connection is broken.
		status := p.streamManager.Status()
		assert.Equal(t, interfaces.DataSourceErrorKindInvalidData, status.LastError.Kind,
			"a refused environment must stay visible in the status")
		assert.Equal(t, interfaces.DataSourceStateValid, status.State,
			"the connection itself is healthy, so the state must not say otherwise")
	})
}

func TestAPutThatRefusesEveryEnvironmentDoesNotReportConfigured(t *testing.T) {
	// Reporting the configuration as complete makes relay answer 401 for every credential it does
	// not recognise. With nothing applied there is no environment to recognise, so 401 would claim
	// the credentials are wrong when the truth is that relay has no configuration to serve. The 503
	// that "not configured" produces is the honest answer, and a load balancer acts on it.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(
			envWithUnusableCredentials(testEnv1),
			envWithUnusableCredentials(testEnv2),
		))

		// ReceivedAllEnvironments arrives on the same channel as the environment messages, so this
		// asserts both that nothing was applied and that the configuration was not declared complete.
		p.requireNoMoreMessages()
	})
}

func TestAStaleSuccessDoesNotOverwriteAConnectionFailure(t *testing.T) {
	// The generation check exists so a success an event would report cannot bury a connection
	// failure that landed while that event was being handled. Every status decision in
	// handleStreamEvent goes through markValidWithError for that reason, including the partial
	// refusal, which used to write its status directly and so reported VALID over a dead
	// connection. The return value is what the retry logic reads: a success that did not apply must
	// not put the stream back on the short retry curve while it is broken.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)
		sm := p.streamManager

		// Get out of INITIALIZING first, which setStatus holds an INTERRUPTED report down to.
		require.True(t, sm.markValidWithError(sm.failureGeneration(), interfaces.DataSourceErrorInfo{}))
		require.Equal(t, interfaces.DataSourceStateValid, sm.Status().State)

		// An event is received, so its handler reads the generation here.
		generation := sm.failureGeneration()

		// The connection then fails while that event is still being handled.
		sm.updateStatus(interfaces.DataSourceStateInterrupted, interfaces.DataSourceErrorInfo{
			Kind: interfaces.DataSourceErrorKindNetworkError,
			Time: time.Now(),
		})
		require.Equal(t, interfaces.DataSourceStateInterrupted, sm.Status().State)

		// The event finishes and reports what it found, on the generation it started with.
		applied := sm.markValidWithError(generation, interfaces.DataSourceErrorInfo{
			Kind: interfaces.DataSourceErrorKindInvalidData,
			Time: time.Now(),
		})

		assert.False(t, applied, "a stale success must not be recorded")
		status := sm.Status()
		assert.Equal(t, interfaces.DataSourceStateInterrupted, status.State,
			"a stale success must not report the connection as working")
		assert.Equal(t, interfaces.DataSourceErrorKindNetworkError, status.LastError.Kind,
			"the connection failure must survive the event's report")
	})
}
