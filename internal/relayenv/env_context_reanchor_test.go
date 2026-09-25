package relayenv

// Tests for the two components a re-anchor has to re-point itself, because SetSDKKey only covers what
// the SDK client owns, plus the accepted-set re-check that guards stream handlers.

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"

	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingBigSegmentSynchronizerFactory records the key each synchronizer was built on, in build
// order, and hands back the synchronizers so a test can check which were started and closed.
type capturingBigSegmentSynchronizerFactory struct {
	mu            sync.Mutex
	keys          []config.SDKKey
	synchronizers []*mockBigSegmentSynchronizer
}

func (f *capturingBigSegmentSynchronizerFactory) create(
	_ httpconfig.HTTPConfig,
	_ bigsegments.BigSegmentStore,
	_ string,
	_ string,
	_ config.EnvironmentID,
	sdkKey config.SDKKey,
	_ *slog.Logger,
	_ string,
) bigsegments.BigSegmentSynchronizer {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &mockBigSegmentSynchronizer{updateCh: make(chan bigsegments.UpdatesSummary)}
	f.keys = append(f.keys, sdkKey)
	f.synchronizers = append(f.synchronizers, s)
	return s
}

func (f *capturingBigSegmentSynchronizerFactory) snapshot() ([]config.SDKKey, []*mockBigSegmentSynchronizer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]config.SDKKey{}, f.keys...), append([]*mockBigSegmentSynchronizer{}, f.synchronizers...)
}

// TestReanchorRebuildsTheBigSegmentSynchronizerOnTheNewKey covers one of the two things SetSDKKey does
// not reach. The synchronizer bakes in its SDK key and its own HTTP configuration at construction and
// is not re-keyable, so a re-anchor has to replace it and retire the previous one.
func TestReanchorRebuildsTheBigSegmentSynchronizerOnTheNewKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}
	factory := &capturingBigSegmentSynchronizerFactory{}

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers: EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:   envConfig,
		AllConfig:   allConfig,
		BigSegmentStoreFactory: func(config.EnvConfig, config.Config, *slog.Logger) (bigsegments.BigSegmentStore, error) {
			return bigsegments.NewNullBigSegmentStore(), nil
		},
		BigSegmentSynchronizerFactory: factory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	keys, syncs := factory.snapshot()
	require.Equal(t, []config.SDKKey{envConfig.SDKKey}, keys)
	require.Len(t, syncs, 1)
	require.False(t, syncs[0].isStarted(), "the synchronizer waits until a big segment is seen")

	newKey := config.SDKKey("rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, newKey, "", ""), time.Unix(1000, 0))

	keys, syncs = factory.snapshot()
	assert.Equal(t, []config.SDKKey{envConfig.SDKKey, newKey}, keys,
		"the re-anchor must build a synchronizer on the new key")
	require.Len(t, syncs, 2)
	assert.True(t, syncs[0].isClosed(), "the outgoing synchronizer must be closed")
	assert.False(t, syncs[1].isClosed())
	assert.False(t, syncs[1].isStarted(),
		"the replacement is only started when the one it replaced had been started")
}

// TestReanchorStartsTheReplacementSynchronizerWhenTheOutgoingOneWasRunning is the other half: once a
// big segment has been seen the synchronizer is running, and the replacement has to pick that up or
// big segment data stops being synchronized after a key rotation.
func TestReanchorStartsTheReplacementSynchronizerWhenTheOutgoingOneWasRunning(t *testing.T) {
	envConfig := st.EnvMain.Config
	factory := &capturingBigSegmentSynchronizerFactory{}

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers: EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:   envConfig,
		AllConfig:   config.Config{},
		BigSegmentStoreFactory: func(config.EnvConfig, config.Config, *slog.Logger) (bigsegments.BigSegmentStore, error) {
			return bigsegments.NewNullBigSegmentStore(), nil
		},
		BigSegmentSynchronizerFactory: factory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	impl := env.(*envContextImpl)
	impl.setBigSegmentsExist()

	_, syncs := factory.snapshot()
	require.Len(t, syncs, 1)
	require.True(t, syncs[0].isStarted())

	impl.reconcileCredentials(mustAcceptedSet(t, config.SDKKey("rotated"), "", ""), time.Unix(1000, 0))

	_, syncs = factory.snapshot()
	require.Len(t, syncs, 2)
	assert.True(t, syncs[1].isStarted(), "the replacement must keep synchronizing after the rotation")
	assert.True(t, syncs[0].isClosed())
}

// TestStreamHandlerRefusesARevokedCredential covers the accepted-set re-check.
//
// The middleware authenticates once, at the start of a request, and the stream providers only
// type-check the credential they are given. On the REPORT stream endpoints the client paces the body
// read that precedes the handler lookup, so a revocation can land in between. Without the re-check a
// revoked credential would be handed a working handler rather than a 404.
func TestStreamHandlerRefusesARevokedCredential(t *testing.T) {
	envConfig := st.EnvMain.Config
	jsClientStreams := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        envConfig,
		AllConfig:        config.Config{},
		ClientFactory:    testclient.FakeLDClientFactory(true),
		StreamProviders:  []streams.StreamProvider{jsClientStreams},
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	impl := env.(*envContextImpl)
	firstEnvID := config.EnvironmentID("env-id-one")
	secondEnvID := config.EnvironmentID("env-id-two")

	// While the environment ID is accepted, the client-side ping provider gives it a real handler.
	env.ReconcileCredentials(mustAcceptedSet(t, envConfig.SDKKey, "", firstEnvID))
	require.True(t, impl.acceptsForStream(firstEnvID))
	assert.NotNil(t, env.GetStreamHandlerV1(jsClientStreams, firstEnvID))
	assertRefusesStream(t, env, jsClientStreams, secondEnvID)

	// Rotating the environment ID revokes the previous one.
	env.ReconcileCredentials(mustAcceptedSet(t, envConfig.SDKKey, "", secondEnvID))

	assert.False(t, impl.acceptsForStream(firstEnvID),
		"a credential the environment no longer accepts must not pass the re-check")
	assertRefusesStream(t, env, jsClientStreams, firstEnvID)
	assert.True(t, impl.acceptsForStream(secondEnvID))
}

// assertRefusesStream checks that both stream paths answer 404 for cred, which is how a credential
// the environment does not accept is turned away.
func assertRefusesStream(t *testing.T, env EnvContext, sp streams.StreamProvider, cred credential.SDKCredential) {
	t.Helper()
	for _, h := range []http.Handler{env.GetStreamHandlerV1(sp, cred), env.GetStreamHandlerV2(sp, cred)} {
		require.NotNil(t, h)
		rec := httptest.NewRecorder()
		req, err := http.NewRequest("GET", "/", nil)
		require.NoError(t, err)
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	}
}

// recordingConnectionMapper records the credential mappings an environment adds and removes, so a
// test can assert which credentials can currently reach the environment.
type recordingConnectionMapper struct {
	mu      sync.Mutex
	added   []credential.SDKCredential
	removed []credential.SDKCredential
}

func (m *recordingConnectionMapper) AddConnectionMapping(cred credential.SDKCredential, _ EnvContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, cred)
}

func (m *recordingConnectionMapper) RemoveConnectionMapping(cred credential.SDKCredential) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, cred)
}

// active reports the credentials that were added and not later removed. An environment's initial
// credentials are mapped by the relay rather than through this interface, so active covers only what
// a reconcile did.
func (m *recordingConnectionMapper) active() []credential.SDKCredential {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []credential.SDKCredential
	for _, cred := range m.added {
		if !slices.Contains(m.removed, cred) {
			out = append(out, cred)
		}
	}
	return out
}

func (m *recordingConnectionMapper) wasAdded(cred credential.SDKCredential) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.added, cred)
}

func (m *recordingConnectionMapper) wasRemoved(cred credential.SDKCredential) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.removed, cred)
}

func TestAFailedReanchorLeavesNoMappingForTheKeyItCouldNotMoveTo(t *testing.T) {
	// reanchor registers a brand-new anchor's mappings before it re-keys the client, because a key
	// cannot serve before its mappings exist. When the re-key fails, those mappings have to come back
	// down. Leaving them would let an SDK authenticate with a key this environment never moved to,
	// and be served by a client still connected on the previous one.
	envConfig := st.EnvMain.Config
	mapper := &recordingConnectionMapper{}
	clientCh := make(chan *testclient.FakeLDClient, 1)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        envConfig,
		AllConfig:        config.Config{},
		ClientFactory:    testclient.FakeLDClientFactoryWithChannel(true, clientCh, nil),
		ConnectionMapper: mapper,
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	client := helpers.RequireValue(t, clientCh, time.Second, "timed out waiting for the client")
	client.SetSDKKeyErr = errors.New("SDK key contains invalid characters")

	refused := config.SDKKey("sdk-refused")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, refused, "", ""), time.Unix(1000, 0))

	// The sequence is the invariant: the new anchor's mappings go up before the re-key, and the
	// rollback takes them back down.
	assert.True(t, mapper.wasAdded(refused), "reanchor registers the new anchor before re-keying")
	assert.True(t, mapper.wasRemoved(refused), "the rollback must unregister it again")
	assert.NotContains(t, mapper.active(), refused,
		"a key the client refused must not be left able to reach the environment")

	assert.False(t, mapper.wasRemoved(envConfig.SDKKey),
		"the previous anchor keeps serving, because it is what the client is still connected on")
	assert.True(t, env.(*envContextImpl).keyRotator.IsAccepted(envConfig.SDKKey),
		"the previous anchor must stay accepted after a rolled-back re-anchor")
	assert.Equal(t, envConfig.SDKKey, env.(*envContextImpl).keyRotator.AnchorKey(),
		"the rotator must stay on the anchor the client actually uses")
	assert.Equal(t, envConfig.SDKKey, client.CurrentSDKKey())
}

func TestAnAnchorThatMovesDuringTheClientBuildIsAppliedWhenTheBuildFinishes(t *testing.T) {
	// A reconcile can land while the initial client build is still running. The build used the key it
	// was started with, so without a catch-up the client would connect on a key the rotator has
	// already moved off, and the environment would serve from a superseded connection.
	envConfig := st.EnvMain.Config
	// An unbuffered channel holds the factory inside the send, which keeps the build in flight for as
	// long as the test wants.
	clientCh := make(chan *testclient.FakeLDClient)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:        envConfig,
		AllConfig:        config.Config{},
		ClientFactory:    testclient.FakeLDClientFactoryWithChannel(true, clientCh, nil),
		ConnectionMapper: mockConnectionMapper{},
		Logger:           slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	rotated := config.SDKKey("sdk-rotated")
	env.(*envContextImpl).reconcileCredentials(mustAcceptedSet(t, rotated, "", ""), time.Unix(1000, 0))
	require.Equal(t, rotated, env.(*envContextImpl).keyRotator.AnchorKey(),
		"with no client yet, the anchor moves without a re-key")

	// Let the build finish, now that the anchor has already moved.
	client := helpers.RequireValue(t, clientCh, time.Second, "timed out waiting for the client")

	require.Eventually(t, func() bool {
		return client.CurrentSDKKey() == rotated
	}, time.Second, time.Millisecond, "the newly built client must be caught up to the current anchor")
}
