package relayenv

// Re-anchor init tolerance.
//
// The SDK reports two different not-initialized outcomes, and the re-anchor treats them differently:
//
//   - ErrInitializationTimeout, or a nil error with the client not yet connected, means the SDK keeps
//     trying. The re-anchor commits that client. The shared data store keeps serving until the client
//     connects, and the timeout goes into initErr. Only the request middleware and the startup wait
//     loop read initErr, and the middleware rejects ErrInitializationFailed alone.
//   - ErrInitializationFailed means the SDK stopped the data source and makes no more attempts. The
//     re-anchor rolls back, because committing would close a working client and install a dead one.
//
// A rollback is expensive: the autoconfiguration layer records the payload's version before the
// re-anchor runs, so a discarded rotation is never re-sent. That is why only a permanent failure
// rolls back.
//
// These tests use fake clients, so Initialized() reports the flag the fake was built with. The real
// client behaves differently on a re-anchor: it inherits the populated store, which gives it Cached
// data availability, so Initialized() returns true before its own data source connects. Relay's
// commit decision does not read Initialized(), so that difference does not affect what these tests
// pin down.

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/store"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const initTolerKeyB = config.SDKKey("init-toler-anchor-b")
const initTolerKeyC = config.SDKKey("init-toler-anchor-c")

// initTolerFactory serves an initialized client for every key except targetKey, and reports those
// clients on healthyCh. For targetKey it returns a non-nil client that is not initialized, paired
// with targetErr, and reports it on targetCh.
//
// It builds the data store for targetKey too. The real SDK builds the store before it waits for the
// initial payload, so a client that times out still holds a store reference.
func initTolerFactory(
	targetKey config.SDKKey,
	targetErr error,
	healthyCh chan *testclient.FakeLDClient,
	targetCh chan *testclient.FakeLDClient,
) sdks.ClientFactoryFunc {
	healthy := testclient.FakeLDClientFactoryWithChannel(true, healthyCh)
	return func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey != targetKey {
			return healthy(sdkKey, cfg, timeout)
		}
		if adapter, ok := cfg.DataStore.(*store.SSERelayDataStoreAdapter); ok {
			if _, err := adapter.Build(subsystems.BasicClientContext{SDKKey: string(sdkKey)}); err != nil {
				return nil, err
			}
		}
		client := &testclient.FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{})}
		targetCh <- client
		return client, targetErr
	}
}

// initTolerEnv starts an environment whose new-anchor build produces an unconverged client, and
// returns the env plus the initial anchor's client.
func initTolerEnv(
	t *testing.T,
	targetErr error,
	mockLog *ldlogtest.MockLog,
	targetCh chan *testclient.FakeLDClient,
) (EnvContext, *testclient.FakeLDClient) {
	t.Helper()
	healthyCh := make(chan *testclient.FakeLDClient, 10)
	factory := initTolerFactory(initTolerKeyB, targetErr, healthyCh, targetCh)

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, st.EnvMain.Config, factory, mockLog.Loggers, readyCh)
	require.Equal(t, env, requireEnvReady(t, readyCh))
	oldClient := requireClientReady(t, healthyCh)
	require.Eventually(t, func() bool { return env.GetClient() == oldClient }, time.Second, 10*time.Millisecond)
	return env, oldClient
}

// TestReanchorInitTolerance_TimeoutCommits proves that a timed-out build still moves the anchor. The
// SDK retries in the background, so relay keeps the new key and records the timeout. It must not
// record ErrInitializationFailed, because the request middleware rejects only that value.
func TestReanchorInitTolerance_TimeoutCommits(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	targetCh := make(chan *testclient.FakeLDClient, 10)
	env, oldClient := initTolerEnv(t, ld.ErrInitializationTimeout, mockLog, targetCh)
	defer env.Close()

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, initTolerKeyB, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	newClient := requireClientReady(t, targetCh)

	assert.Equal(t, initTolerKeyB, env.(*envContextImpl).keyRotator.AnchorKey(),
		"the anchor moves even though the client is still connecting")
	assert.Same(t, newClient, env.GetClient(), "the unconverged client backs the anchor")
	assert.False(t, env.GetClient().Initialized(), "sanity: this fake reports itself as not initialized")
	assert.Equal(t, ld.ErrInitializationTimeout, env.GetInitError(),
		"the timeout is recorded rather than discarded")
	assert.NotEqual(t, ld.ErrInitializationFailed, env.GetInitError(),
		"the middleware rejects only ErrInitializationFailed, so it must not appear here")
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Re-anchored SDK from .* the client is not initialized yet")

	oldClient.AwaitClose(t, time.Second)
}

// TestReanchorInitTolerance_PermanentFailureRollsBack covers the shape the SDK uses for an
// unrecoverable status such as 401: a non-nil client that is not initialized, with
// ErrInitializationFailed. The SDK stops the data source, so there is nothing to wait for. The
// re-anchor must keep the previous anchor and its working client.
func TestReanchorInitTolerance_PermanentFailureRollsBack(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	targetCh := make(chan *testclient.FakeLDClient, 10)
	env, oldClient := initTolerEnv(t, ld.ErrInitializationFailed, mockLog, targetCh)
	defer env.Close()

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, initTolerKeyB, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	rejected := requireClientReady(t, targetCh)

	assert.Equal(t, envConfig.SDKKey, env.(*envContextImpl).keyRotator.AnchorKey(),
		"the anchor stays on the previous key")
	assert.Same(t, oldClient, env.GetClient(), "the previous anchor's client still serves")
	assert.NoError(t, env.GetInitError(), "a rollback leaves the serving env's init status alone")
	mockLog.AssertMessageMatch(t, true, ldlog.Error, "Re-anchor to SDK key .* failed")

	rejected.AwaitClose(t, time.Second)
	select {
	case <-oldClient.CloseCh:
		t.Fatal("the previous anchor's client must stay open after a rollback")
	default:
	}

	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, installed := envImpl.clients[initTolerKeyB]
	envImpl.mu.RUnlock()
	assert.False(t, installed, "the permanently failed client must not be installed")
}

// TestReanchorInitTolerance_UninitializedWithNilErrorCommits covers a zero InitTimeout. The SDK
// constructor returns at once with a client that has not connected and a nil error, and the client
// connects in the background. The re-anchor must commit it.
//
// The previous guard rejected this shape. In production it only bit when the data store was not yet
// initialized, because a handed-over populated store makes the real client report itself initialized.
func TestReanchorInitTolerance_UninitializedWithNilErrorCommits(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	targetCh := make(chan *testclient.FakeLDClient, 10)
	env, oldClient := initTolerEnv(t, nil, mockLog, targetCh)
	defer env.Close()

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, initTolerKeyB, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	newClient := requireClientReady(t, targetCh)

	assert.Equal(t, initTolerKeyB, env.(*envContextImpl).keyRotator.AnchorKey(), "the anchor moves")
	assert.Same(t, newClient, env.GetClient(), "the unconverged client backs the anchor")
	assert.NoError(t, env.GetInitError(), "a nil build error records no init error, as at startup")

	oldClient.AwaitClose(t, time.Second)
}

// TestReanchorInitTolerance_HealthyReanchorClearsRecordedTimeout proves the recorded timeout is not
// sticky. commitReanchor records each build's outcome, so a later healthy re-anchor replaces the
// timeout with nil.
func TestReanchorInitTolerance_HealthyReanchorClearsRecordedTimeout(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	targetCh := make(chan *testclient.FakeLDClient, 10)
	env, _ := initTolerEnv(t, ld.ErrInitializationTimeout, mockLog, targetCh)
	defer env.Close()

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, initTolerKeyB, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	require.Equal(t, ld.ErrInitializationTimeout, env.GetInitError(), "sanity: the timeout was recorded")

	// Key C is not the factory's target key, so its build produces an initialized client.
	reanchorViaReconcile(t, env, initTolerKeyC, initTolerKeyB, "", envConfig.MobileKey, envConfig.EnvID, now)

	assert.Equal(t, initTolerKeyC, env.(*envContextImpl).keyRotator.AnchorKey(), "the anchor moved again")
	assert.NoError(t, env.GetInitError(), "a healthy re-anchor clears the recorded timeout")
	assert.True(t, env.GetClient().Initialized(), "the anchor's client is initialized")
}

// TestReanchorInitTolerance_UnconvergedCommitKeepsStoreServing proves the point of committing an
// unconverged client: the store is handed over, not rebuilt, so relay answers requests from the
// existing data while the new client connects.
func TestReanchorInitTolerance_UnconvergedCommitKeepsStoreServing(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	targetCh := make(chan *testclient.FakeLDClient, 10)
	env, _ := initTolerEnv(t, ld.ErrInitializationTimeout, mockLog, targetCh)
	defer env.Close()

	storeBefore := env.GetStore()
	require.NotNil(t, storeBefore)
	require.NoError(t, storeBefore.Init(st.AllData))
	flagsBefore, err := storeBefore.GetAll(ldstoreimpl.Features())
	require.NoError(t, err)
	require.NotEmpty(t, flagsBefore, "sanity: the store holds data before the re-anchor")

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, initTolerKeyB, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	newClient := requireClientReady(t, targetCh)

	require.Equal(t, initTolerKeyB, env.(*envContextImpl).keyRotator.AnchorKey(),
		"sanity: the unconverged client was committed")
	require.Same(t, newClient, env.GetClient(), "sanity: the unconverged client backs the anchor")

	assert.Same(t, storeBefore, env.GetStore(),
		"the unconverged client shares the previous client's store wrapper")
	flagsAfter, err := env.GetStore().GetAll(ldstoreimpl.Features())
	require.NoError(t, err)
	assert.Len(t, flagsAfter, len(flagsBefore), "the store keeps serving while the new client connects")
	assert.NotNil(t, env.GetEvaluator(), "the evaluator is wired to the handed-over store")
}
