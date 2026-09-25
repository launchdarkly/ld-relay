package autoconfig

// Tests for a payload that parses as JSON but whose credentials cannot produce a usable accepted set.
// The environment keeps the credentials it already had, the stream reconnects to ask again, and the
// cache keeps that environment's last-good entry.

import (
	"context"
	"sync"
	"testing"
	"time"

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
