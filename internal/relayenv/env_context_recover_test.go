package relayenv

// Tests for recovering an environment whose SDK client cannot serve: one whose data system is off,
// one whose build produced no client, and one whose build recorded an initialization error. A
// client is re-keyed in place whatever its status, because LDRelayApplySDKKey restarts a data
// system that is off. Only an environment with no client needs a new one. A client that recovers
// needs its build error cleared.

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingClientFactory hands out a FakeLDClient per build and reports each one on a channel. errs
// gives the error to return from the nth build, so a test can make the first client fail the way the
// SDK does when it is handed a revoked key: a usable client value alongside ErrInitializationFailed.
type recordingClientFactory struct {
	mu     sync.Mutex
	builds int
	errs   []error
	// nilClientBuilds is how many of the first builds return no client at all, alongside their
	// error. That is what the SDK does when it refuses the key at construction: MakeCustomClient
	// returns a nil client, and relay's factory passes the nil straight through.
	nilClientBuilds int
	clientCh        chan *testclient.FakeLDClient
}

func newRecordingClientFactory(errs ...error) *recordingClientFactory {
	return &recordingClientFactory{
		errs:     errs,
		clientCh: make(chan *testclient.FakeLDClient, 4),
	}
}

func (f *recordingClientFactory) create(sdkKey config.SDKKey, cfg ld.Config, _ time.Duration) (sdks.LDClientContext, error) {
	// Relay gets its shared reference to the data store through this hook, as it would from the SDK.
	if cfg.LDRelayDataDestination != nil {
		cfg.LDRelayDataDestination(testclient.NewFakeStore(st.AllData), nil)
	}

	f.mu.Lock()
	f.builds++
	build := f.builds
	var err error
	if build <= len(f.errs) {
		err = f.errs[build-1]
	}
	f.mu.Unlock()

	if build <= f.nilClientBuilds {
		return nil, err
	}

	client := &testclient.FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{})}
	f.clientCh <- client
	return client, err
}

func (f *recordingClientFactory) awaitClient(t *testing.T, msg string) *testclient.FakeLDClient {
	t.Helper()
	return helpers.RequireValue(t, f.clientCh, time.Second, msg)
}

func newRecoverTestEnv(t *testing.T, factory *recordingClientFactory) EnvContext {
	t.Helper()
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        st.EnvMain.Config,
		AllConfig:        config.Config{},
		ClientFactory:    factory.create,
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Close() })
	return env
}

func TestReanchorRekeysAClientWhoseDataSystemIsOff(t *testing.T) {
	// A revoked key makes every synchronizer fail, and the SDK then reports Off. LDRelayApplySDKKey
	// restarts those synchronizers on the new key, so relay re-keys the client and the environment
	// keeps its store. A rebuild would drop the store and refuse requests until the new client had
	// data.
	factory := newRecordingClientFactory()
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateOff})

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	assert.Equal(t, []config.SDKKey{rotated}, first.SDKKeys(), "the client must be re-keyed")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"a client whose data system is off must be re-keyed rather than rebuilt") {
		t.FailNow()
	}
	assert.Equal(t, first, env.GetClient())
}

func TestReanchorRekeysAHealthyClientRatherThanRebuildingIt(t *testing.T) {
	// The whole point of the in-place re-key is that a healthy environment keeps its store, so this
	// pins that the recovery path does not take over the ordinary rotation.
	factory := newRecordingClientFactory()
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateValid})

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	assert.Equal(t, []config.SDKKey{rotated}, first.SDKKeys(), "the live client must be re-keyed")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"a healthy environment must not have its client rebuilt") {
		t.FailNow()
	}
	assert.Equal(t, first, env.GetClient())
}

func TestARevokedKeyEnvironmentServesOnceTheReanchoredClientReceivesData(t *testing.T) {
	// An environment created on a key LaunchDarkly has already revoked. Every synchronizer fails on
	// the authorization error, so the build records ErrInitializationFailed and the SDK reports Off.
	// The re-anchor re-keys the client in place, and LDRelayApplySDKKey restarts the synchronizers.
	//
	// The error must stay until the client has data, because a restarted client may hold an empty
	// store and relay would answer 200 with no flags rather than refusing. It must clear once the
	// client has data, because the middleware rejects every request while it is set.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateOff})
	require.Eventually(t, func() bool {
		return env.GetInitError() != nil
	}, time.Second, time.Millisecond, "the revoked key must record an initialization error")

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	assert.Equal(t, []config.SDKKey{rotated}, first.SDKKeys(), "the client must be re-keyed")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"a client whose data system is off must be re-keyed rather than rebuilt") {
		t.FailNow()
	}
	assert.ErrorIs(t, env.GetInitError(), ld.ErrInitializationFailed,
		"an environment whose client has not received data must keep refusing requests")

	// The synchronizers restart on the new key. While they have no data, the error stays.
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateInterrupted})
	assert.ErrorIs(t, env.GetInitError(), ld.ErrInitializationFailed,
		"an environment whose client has not received data must keep refusing requests")

	// The synchronizers receive data.
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateValid})
	assert.NoError(t, env.GetInitError(), "the environment must serve once its client receives data")
}

func TestTheInitializationErrorClearsOnceTheClientReceivesData(t *testing.T) {
	// SDK-3209. A client can receive data after its build recorded an error, with no re-anchor at
	// all. The error must not outlive that. A later interruption must not bring it back either,
	// because the store then holds data.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	require.Eventually(t, func() bool {
		return env.GetInitError() != nil
	}, time.Second, time.Millisecond, "the failed build must record an initialization error")

	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateValid})
	assert.NoError(t, env.GetInitError(), "a client that is receiving data must not stay marked as failed")

	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateInterrupted})
	assert.NoError(t, env.GetInitError(),
		"an interruption after the client received data must not bring the error back")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"clearing the error must not rebuild the client") {
		t.FailNow()
	}
}

func TestAClientBuiltAfterTheEnvironmentClosedIsDiscarded(t *testing.T) {
	// There are two launch sites for a build now, the initial one and the rebuild, so a build
	// finishing after Close is reachable in a way it was not before. Installing that client would
	// leak its connection, goroutines and store, and leave GetClient answering for an environment
	// the relay has deleted.
	factory := newRecordingClientFactory()
	// An unbuffered channel holds the factory inside its send, which keeps the build in flight for
	// as long as the test wants.
	factory.clientCh = make(chan *testclient.FakeLDClient)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        st.EnvMain.Config,
		AllConfig:        config.Config{},
		ClientFactory:    factory.create,
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)

	require.NoError(t, env.Close())

	// Let the build finish, now that the environment is gone.
	client := factory.awaitClient(t, "timed out waiting for the in-flight client")
	client.AwaitClose(t, time.Second)
	assert.Nil(t, env.GetClient(), "a closed environment must not acquire a client")
}

func TestReanchorBuildsAClientWhenTheEnvironmentNeverGotOne(t *testing.T) {
	// The SDK refuses a key it cannot send in a header at construction too, not just on a re-key,
	// and then MakeCustomClient returns no client at all. Phase 3 removed the path that used to
	// build a client for a newly accepted key, so without this the environment would stay unserved
	// for the life of the process even once auto-configuration delivered a usable key.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	factory.nilClientBuilds = 1
	env := newRecoverTestEnv(t, factory)

	require.Eventually(t, func() bool {
		return env.GetInitError() != nil && env.GetClient() == nil
	}, time.Second, time.Millisecond, "the failed build must leave no client and record an error")

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	built := factory.awaitClient(t, "timed out waiting for a client to be built on the new anchor")
	assert.Equal(t, rotated, built.Key)
	require.Eventually(t, func() bool {
		return env.GetClient() == built && env.GetInitError() == nil
	}, time.Second, time.Millisecond, "the environment must serve once it has a usable client")
}

func TestAnAnchorThatMovesTwiceDuringARebuildStartsOnlyOneClient(t *testing.T) {
	// A reconcile landing while a rebuild is running must not start a second one: the environment
	// would end up with two clients, each with its own store and upstream connection. The client
	// that the rebuild installs must end on the anchor that the second reconcile committed.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	factory.nilClientBuilds = 1
	// An unbuffered channel holds the factory inside its send, which keeps the rebuild in flight
	// for both reconciles. The initial build returns no client, so it does not send.
	factory.clientCh = make(chan *testclient.FakeLDClient)
	env := newRecoverTestEnv(t, factory)

	require.Eventually(t, func() bool {
		return env.GetInitError() != nil && env.GetClient() == nil
	}, time.Second, time.Millisecond, "the failed build must leave no client")

	impl := env.(*envContextImpl)
	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("sdk-first"), "", ""), time.Unix(1000, 0))
	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("sdk-second"), "", ""), time.Unix(2000, 0))

	// Let the rebuild finish. It was started on the first key and catches up to the second.
	built := factory.awaitClient(t, "timed out waiting for the rebuilt client")
	require.Eventually(t, func() bool {
		return env.GetClient() == built
	}, time.Second, time.Millisecond, "the rebuilt client must become the environment's client")
	assert.Equal(t, []config.SDKKey{"sdk-second"}, built.SDKKeys(),
		"the rebuilt client must catch up to the anchor that the second reconcile committed")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 200*time.Millisecond,
		"a second reconcile must not start a concurrent build") {
		t.FailNow()
	}
}

func TestAReanchorAsARebuildFinishesRekeysTheNewClient(t *testing.T) {
	// rebuildSDKClient clears clientRebuilding after startSDKClient has installed the client, run
	// its catch-up and released c.mu. A reconcile in that window must still re-key the installed
	// client. Otherwise the anchor moves while the client stays on the superseded key.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	factory.nilClientBuilds = 1
	// startSDKClient logs this after it releases c.mu and before rebuildSDKClient clears the flag.
	// Holding that log call keeps the environment in the window.
	held := &holdingLogHandler{
		Handler: slog.Default().Handler(),
		message: "initialized LaunchDarkly client",
		held:    make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        st.EnvMain.Config,
		AllConfig:        config.Config{},
		ClientFactory:    factory.create,
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.New(held),
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Close() })
	require.Eventually(t, func() bool {
		return env.GetInitError() != nil && env.GetClient() == nil
	}, time.Second, time.Millisecond, "the failed build must leave no client")

	impl := env.(*envContextImpl)
	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("sdk-first"), "", ""), time.Unix(1000, 0))
	built := factory.awaitClient(t, "timed out waiting for the rebuilt client")
	helpers.RequireValue(t, held.held, time.Second, "timed out waiting for the rebuild to install its client")

	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("sdk-second"), "", ""), time.Unix(2000, 0))
	close(held.release)

	assert.Equal(t, config.SDKKey("sdk-second"), env.GetAnchorKey())
	assert.Equal(t, []config.SDKKey{"sdk-second"}, built.SDKKeys(),
		"the installed client must be re-keyed to the anchor that the reconcile committed")
}

// holdingLogHandler holds the first goroutine that logs message until release is closed, and
// signals held when it does.
type holdingLogHandler struct {
	slog.Handler
	message string
	held    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *holdingLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.message {
		h.once.Do(func() {
			h.held <- struct{}{}
			<-h.release
		})
	}
	return h.Handler.Handle(ctx, r)
}

func (h *holdingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *holdingLogHandler) WithGroup(string) slog.Handler { return h }

func TestTheRetryAppliesTheKeyAgainWhileTheDataSystemIsOff(t *testing.T) {
	// Every synchronizer failed permanently, and the anchor has not changed. Only an
	// LDRelayApplySDKKey call starts the synchronizers again, so the cleanup ticker applies the
	// current key again on each tick until the client receives data.
	factory := newRecordingClientFactory()
	env := newRecoverTestEnv(t, factory)
	impl := env.(*envContextImpl)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateOff})
	anchor := env.GetAnchorKey()

	impl.retryAnOffClient()
	impl.retryAnOffClient()
	assert.Equal(t, []config.SDKKey{anchor, anchor}, first.SDKKeys(),
		"each tick must apply the current key again while the data system is off")

	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateValid})
	impl.retryAnOffClient()
	assert.Len(t, first.SDKKeys(), 2, "a client that receives data must not be re-keyed again")

	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"the retry must re-key the client rather than rebuild it") {
		t.FailNow()
	}
	assert.Equal(t, first, env.GetClient())
}

func TestTheRetryLeavesAClientAloneUnlessItsDataSystemIsOff(t *testing.T) {
	// A client in any other state either has data or is still trying on its own. Re-keying it would
	// make it restart a synchronizer that has not failed.
	factory := newRecordingClientFactory()
	env := newRecoverTestEnv(t, factory)
	impl := env.(*envContextImpl)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	for _, state := range []interfaces.DataSourceState{
		interfaces.DataSourceStateInitializing,
		interfaces.DataSourceStateValid,
		interfaces.DataSourceStateInterrupted,
	} {
		first.SetDataSourceStatus(interfaces.DataSourceStatus{State: state})
		impl.retryAnOffClient()
		assert.Empty(t, first.SDKKeys(), "a client that is %s must not be re-keyed", state)
	}
}

func TestTheRetryDoesNothingForAnEnvironmentWithNoClient(t *testing.T) {
	// An environment whose build produced no client gets one from the re-anchor, not from the retry.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	factory.nilClientBuilds = 1
	env := newRecoverTestEnv(t, factory)
	require.Eventually(t, func() bool {
		return env.GetInitError() != nil && env.GetClient() == nil
	}, time.Second, time.Millisecond, "the failed build must leave no client")

	env.(*envContextImpl).retryAnOffClient()

	assert.Nil(t, env.GetClient())
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"the retry must not build a client") {
		t.FailNow()
	}
}
