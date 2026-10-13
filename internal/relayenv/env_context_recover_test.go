package relayenv

// Tests for recovering an environment whose SDK client cannot serve: one whose data system has
// stopped for good, one whose build produced no client, and one whose build recorded an
// initialization error. Re-keying reaches only the credential the client's requests carry, so a
// stopped data system needs a new client. A client that recovers needs its build error cleared.

import (
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

func TestReanchorRebuildsTheClientWhenTheDataSystemHasShutDown(t *testing.T) {
	// A data system that reports Off has stopped for good. Re-keying that client changes a header on
	// a connection nothing is driving any more, so relay would log a move that did not happen and
	// serve whatever the store still held, forever.
	factory := newRecordingClientFactory()
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateOff})

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	first.AwaitClose(t, time.Second)
	second := factory.awaitClient(t, "timed out waiting for the rebuilt client")
	assert.Equal(t, rotated, second.Key, "the replacement must be built on the new anchor")
	assert.Empty(t, first.SDKKeys(), "a client with a dead data system must not be re-keyed instead")

	require.Eventually(t, func() bool {
		return env.GetClient() == second
	}, time.Second, time.Millisecond, "the rebuilt client must become the environment's client")
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

func TestTheRebuildClearsTheInitializationErrorOfAStoppedClient(t *testing.T) {
	// A build that records ErrInitializationFailed makes the middleware reject every request for the
	// environment on that error alone. When the client's data system has also stopped for good, it
	// can never receive data to clear the error, so the rebuild replaces both the client and the
	// error.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateOff})
	require.Eventually(t, func() bool {
		return env.GetInitError() != nil
	}, time.Second, time.Millisecond, "the failed build must record an initialization error")

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	second := factory.awaitClient(t, "timed out waiting for the rebuilt client")
	assert.Equal(t, rotated, second.Key)
	require.Eventually(t, func() bool {
		return env.GetInitError() == nil
	}, time.Second, time.Millisecond,
		"the environment must serve again once it has a client built on a valid key")
}

func TestARevokedKeyEnvironmentServesOnceTheReanchoredClientReceivesData(t *testing.T) {
	// An environment created on a key LaunchDarkly has already revoked. Every synchronizer fails on
	// the authorization error, so the build records ErrInitializationFailed. The SDK reports
	// Interrupted, not Off, because it restarts the synchronizers once SetSDKKey supplies a new key.
	// So the re-anchor re-keys the client in place.
	//
	// The error must stay until the client has data, because an interrupted client may hold an empty
	// store and relay would answer 200 with no flags rather than refusing. It must clear once the
	// client has data, because the middleware rejects every request while it is set.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	env := newRecoverTestEnv(t, factory)

	first := factory.awaitClient(t, "timed out waiting for the initial client")
	first.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateInterrupted})
	require.Eventually(t, func() bool {
		return env.GetInitError() != nil
	}, time.Second, time.Millisecond, "the revoked key must record an initialization error")

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))

	assert.Equal(t, []config.SDKKey{rotated}, first.SDKKeys(), "the interrupted client must be re-keyed")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 100*time.Millisecond,
		"an interrupted client must be re-keyed rather than rebuilt") {
		t.FailNow()
	}
	assert.ErrorIs(t, env.GetInitError(), ld.ErrInitializationFailed,
		"an environment whose client has not received data must keep refusing requests")

	// The synchronizers restart on the new key and receive data.
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
	// would end up with two clients, each with its own store and upstream connection.
	factory := newRecordingClientFactory(ld.ErrInitializationFailed)
	factory.nilClientBuilds = 1
	env := newRecoverTestEnv(t, factory)

	require.Eventually(t, func() bool {
		return env.GetInitError() != nil && env.GetClient() == nil
	}, time.Second, time.Millisecond, "the failed build must leave no client")

	impl := env.(*envContextImpl)
	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("sdk-first"), "", ""), time.Unix(1000, 0))
	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("sdk-second"), "", ""), time.Unix(2000, 0))

	_ = factory.awaitClient(t, "timed out waiting for the rebuilt client")
	if !helpers.AssertNoMoreValues(t, factory.clientCh, 200*time.Millisecond,
		"a second reconcile must not start a concurrent build") {
		t.FailNow()
	}
}
