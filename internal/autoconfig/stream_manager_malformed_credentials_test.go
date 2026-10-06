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
	"log/slog"
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
	refused := envWithUnusableCredentials(testEnv1)
	refused.Version = testEnv1.Version + 1

	// The service replays the same version on the reconnect, this time in a shape relay can use.
	// Serving it as the second connection's initial event, rather than enqueueing it after the
	// refusal, guarantees it reaches the new connection. An enqueued event can be written to the old
	// connection before the server notices it closed, and is then lost.
	replayed := testEnv1
	replayed.Version = testEnv1.Version + 1
	replayed.EnvName = "renamed-by-the-replay"
	replayEvent := makeEnvPutEvent(replayed)

	firstHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()
	replayHandler, replayStream := httphelpers.SSEHandler(&replayEvent)
	defer replayStream.Close()

	handler := httphelpers.SequentialHandler(firstHandler, replayHandler)
	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		// Establish the environment, then refuse an update at version 11.
		p.stream.Enqueue(makeEnvPutEvent(testEnv1))
		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		p.requireReceivedAllMessage()

		// The reconnect is immediate, so the replay can arrive within any quiet window this test
		// could wait for. The refused patch handing anything to the relay shows up instead as a first
		// message that is not the replay.
		p.stream.Enqueue(makePatchEnvEvent(refused))

		// The refusal reconnects, and the replay must be applied rather than deduplicated away.
		awaitStreamRequest(t, p)
		msg = p.requireMessage()
		require.NotNil(t, msg.update, "the replay of a refused version must be applied")
		assert.Equal(t, "renamed-by-the-replay", msg.update.Identifiers.EnvName)
		p.requireReceivedAllMessage()
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
	// blockFirstGet holds the startup read open until its context is cancelled. That is what the
	// stream does in production when its first put wins the race, so the cached data never reaches
	// the relay by that route. The put path's own read is left alone.
	blockFirstGet bool
	gets          int
}

func (c *recordingCache) GetAll(ctx context.Context) (*PutContent, error) {
	c.mu.Lock()
	block := c.blockFirstGet && c.gets == 0
	c.gets++
	previous, getErr := c.previous, c.getErr
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return previous, getErr
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

func TestRepeatedRefusalsStayOnTheShortRetryCurve(t *testing.T) {
	// Unusable data restarts the stream on the short curve however often it arrives. eventsource
	// grows its own delay for each restart, up to streamMaxRetryDelay, which bounds the reconnects;
	// the extended delays belong to a failing connection. A payload relay cannot use is fixed
	// upstream, and a stream parked on the extended delays is disconnected, so it cannot learn that
	// the payload was fixed.
	const extendedBase = 2 * time.Second

	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = extendedBase
		p.startStream()
		awaitStreamRequest(t, p)

		for i := 0; i < 3; i++ {
			p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))
			awaitStreamRequest(t, p)
		}

		delays := reconnectDelays(p.mockLog)
		require.GreaterOrEqual(t, len(delays), 3, "expected a logged delay for each restart")
		for i, d := range delays {
			assert.Less(t, d, extendedBase/2,
				"restart %d must stay on the short curve", i+1)
		}
	})
}

func TestAUsableEventReturnsTheStreamToTheShortRetryCurve(t *testing.T) {
	// A rejected key engages the extended delays through the stream error handler. eventsource
	// reverts an activated profile only after a stretch of healthy operation, and it measures that
	// stretch from a timestamp stamped as the last event was delivered, so a restart that follows an
	// event can never clear it. Without an explicit revert the stream keeps the extended delays long
	// after the key is valid again, and the next reconnect waits minutes.
	const extendedBase = 300 * time.Millisecond

	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		httphelpers.HandlerWithStatus(401), // the rejected key engages the extended delays
		streamHandler,                      // the retry succeeds and delivers a usable put
	)

	streamManagerTestWithStreamHandler(t, handler, stream, func(p streamManagerTestParams) {
		p.streamManager.extendedRetryDelay = extendedBase
		p.startStream()

		awaitStreamRequest(t, p) // the rejected request
		awaitStreamRequest(t, p) // the retry

		p.requireMessage() // the put the recovered stream delivered
		p.requireReceivedAllMessage()
		require.True(t, p.mockLog.HasMessage(slog.LevelInfo, "engaging extended backoff"),
			"the rejected key must have engaged the extended delays")

		before := len(reconnectDelays(p.mockLog))

		// A refused patch restarts the stream. The delay it waits is the one under test.
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
	// come back identical on a new connection, so a reconnect achieves nothing, and it would take
	// every healthy environment's updates down with it.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))
		msg := p.requireMessage()
		require.NotNil(t, msg.add, "the well-formed environment in the put must be applied")
		p.requireReceivedAllMessage()

		// A second partial refusal must be no different. Bump the good environment so it is a real
		// update rather than a version the receiver dedupes away.
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

func TestAPutWithNoEnvironmentsSaysThatItRemovedEverything(t *testing.T) {
	// An empty configuration is legal, so relay applies it rather than refusing or reconnecting. It
	// still leaves relay answering 401 for every credential, which an operator should not have to
	// infer from an environment count of zero and a run of delete lines.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1))
		p.requireMessage()
		p.requireReceivedAllMessage()

		p.stream.Enqueue(makeEnvPutEvent())
		msg := p.requireMessage()
		require.NotNil(t, msg.delete, "the environment must be removed, got %s", msg)
		p.requireReceivedAllMessage()

		assert.True(t, p.mockLog.HasMessage(slog.LevelWarn, "contains no environments"),
			"emptying the configuration must be reported")
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

func TestRefusedEnvironmentsAreReportedToTheHandler(t *testing.T) {
	// A refused environment is never created, and the readiness bit is still set, so without this
	// signal the only lasting record is a log line. An SDK on that environment's credentials gets a
	// 401 and nothing in the status document explains why.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		// A put reports every environment in it that could not be used.
		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))
		p.requireMessage()
		p.requireReceivedAllMessage()

		assert.Equal(t, map[config.EnvironmentID]string{testEnv2.EnvID: refusedReason},
			p.messageHandler.refusedEnvironments())

		// A later put that carries a usable payload for that environment retires the refusal. The
		// put is authoritative, so the set is replaced rather than added to.
		p.stream.Enqueue(makeEnvPutEvent(testEnv1, testEnv2))
		p.requireMessage()
		p.requireReceivedAllMessage()

		assert.Empty(t, p.messageHandler.refusedEnvironments(),
			"an environment Relay can now serve must not stay listed as refused")
	})
}

func TestAPutThatDropsARefusedEnvironmentRetiresTheRefusal(t *testing.T) {
	// The refused environment was never handed to the receiver, so it is not tracked there and the
	// delete path never fires for it. Only replacing the set on each put retires this one.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))
		p.requireMessage()
		p.requireReceivedAllMessage()
		require.NotEmpty(t, p.messageHandler.refusedEnvironments())

		// testEnv2 is gone from the configuration entirely.
		p.stream.Enqueue(makeEnvPutEvent(testEnv1))
		p.requireReceivedAllMessage()

		assert.Empty(t, p.messageHandler.refusedEnvironments(),
			"an environment no longer in the configuration must not stay listed as refused")
	})
}

func TestARefusedPatchIsReportedToTheHandler(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))

		require.Eventually(t, func() bool {
			return len(p.messageHandler.refusedEnvironments()) == 1
		}, time.Second, 10*time.Millisecond, "a refused patch must be reported")
		assert.Equal(t, refusedReason, p.messageHandler.refusedEnvironments()[testEnv1.EnvID])
	})
}

func TestDeletingAnEnvironmentThatWasOnlyEverRefusedRetiresItsRefusal(t *testing.T) {
	// An environment whose only payload was refused was never handed to the receiver, so a delete
	// for it reports a noop and no delete reaches the handler. Without retiring the refusal on the
	// delete itself, Relay would go on reporting an environment LaunchDarkly has removed as one it
	// is failing to serve, until the next put replaced the whole set -- which on a healthy stream
	// means the next reconnect, not the next minute.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(testEnv1)))
		require.Eventually(t, func() bool {
			return len(p.messageHandler.refusedEnvironments()) == 1
		}, time.Second, 10*time.Millisecond, "the refused patch must be reported")
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version+1))

		require.Eventually(t, func() bool {
			return len(p.messageHandler.refusedEnvironments()) == 0
		}, time.Second, 10*time.Millisecond,
			"a deleted environment must not stay listed as refused")
	})
}

func TestDeletingAnEnvironmentThatWasServingAlsoRetiresItsRefusal(t *testing.T) {
	// The other order: the environment applied, a later patch for it was refused so it kept serving,
	// and then it was deleted. The receiver dispatches a real delete here rather than a noop, so
	// this pins that retiring the refusal does not depend on which of the two the receiver chose.
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makePatchEnvEvent(testEnv1))
		p.requireMessage()

		refused := testEnv1
		refused.Version++
		p.stream.Enqueue(makePatchEnvEvent(envWithUnusableCredentials(refused)))
		require.Eventually(t, func() bool {
			return len(p.messageHandler.refusedEnvironments()) == 1
		}, time.Second, 10*time.Millisecond, "the refused patch must be reported")
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeDeleteEnvEvent(testEnv1.EnvID, refused.Version+1))

		require.Eventually(t, func() bool {
			return len(p.messageHandler.refusedEnvironments()) == 0
		}, time.Second, 10*time.Millisecond,
			"a deleted environment must not stay listed as refused")
	})
}

func TestARefusedEnvironmentIsServedFromItsLastGoodCacheEntry(t *testing.T) {
	// Writing the last-good entry back to the cache serves the next process start. This process has
	// to serve it too, or Relay holds a usable configuration for an environment it answers nothing
	// for -- and since a partial refusal no longer reconnects, the service will not resend it.
	cache := &recordingCache{
		blockFirstGet: true,
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

		// Both environments reach the relay: testEnv1 from the put, testEnv2 from the cache.
		var ids []config.EnvironmentID
		for i := 0; i < 2; i++ {
			msg := p.requireMessage()
			require.NotNil(t, msg.add, "expected an environment to be added, got %s", msg)
			ids = append(ids, msg.add.EnvID)
		}
		assert.Contains(t, ids, testEnv1.EnvID)
		assert.Contains(t, ids, testEnv2.EnvID,
			"the refused environment must be served from its last-good cached entry")
		p.requireReceivedAllMessage()
	})
}

func TestAMalformedCachedEntryIsNotCarriedForward(t *testing.T) {
	// Relay versions before the credential check wrote the cache without validating it, so a store an
	// older relay filled can hold a payload this one refuses. Carrying such an entry forward would
	// create an environment from a payload relay has just declared unusable, and would write the same
	// problem back for the next process start.
	cache := &recordingCache{
		blockFirstGet: true,
		previous: &PutContent{Environments: map[config.EnvironmentID]envfactory.EnvironmentRep{
			testEnv2.EnvID: envWithUnusableCredentials(testEnv2),
		}},
	}

	streamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithCache(t, streamHandler, stream, cache, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		p.stream.Enqueue(makeEnvPutEvent(testEnv1, envWithUnusableCredentials(testEnv2)))

		// Only the well-formed environment from the put arrives. The refused one has no usable entry
		// to fall back to, so nothing is served for it.
		msg := p.requireMessage()
		require.NotNil(t, msg.add, "expected the well-formed environment, got %s", msg)
		assert.Equal(t, testEnv1.EnvID, msg.add.EnvID)
		p.requireReceivedAllMessage()

		require.Eventually(t, func() bool { _, ok := cache.lastWrite(); return ok },
			time.Second, time.Millisecond, "timed out waiting for the cache write")
		written, _ := cache.lastWrite()
		assert.NotContains(t, written.Environments, testEnv2.EnvID,
			"a malformed cached entry must not be written back")

		assert.True(t, p.mockLog.HasMessage(slog.LevelError, "nothing to fall back to"),
			"the unusable fallback must be reported")
	})
}

func TestAFullyRefusedPutStillReportsConfiguredWhenTheCacheCoversIt(t *testing.T) {
	// The readiness gate exists so Relay does not claim to be configured while serving nothing. An
	// environment recovered from the cache is being served, so it counts -- otherwise Relay would
	// answer 503 for an environment whose flags it can serve perfectly well.
	cache := &recordingCache{
		blockFirstGet: true,
		previous: &PutContent{Environments: map[config.EnvironmentID]envfactory.EnvironmentRep{
			testEnv1.EnvID: testEnv1,
		}},
	}

	streamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithCache(t, streamHandler, stream, cache, func(p streamManagerTestParams) {
		p.startStream()
		awaitStreamRequest(t, p)

		// Every environment in the put is refused, so nothing of its own data applies.
		p.stream.Enqueue(makeEnvPutEvent(envWithUnusableCredentials(testEnv1)))

		msg := p.requireMessage()
		require.NotNil(t, msg.add, "the cached entry must be served, got %s", msg)
		assert.Equal(t, testEnv1.EnvID, msg.add.EnvID)
		p.requireReceivedAllMessage()
	})
}
