package relayenv

// T0 — Re-anchoring PoC (SDK-2453 / SDK-2530).
//
// These tests validate the upstream SDK-client swap mechanism that T2.c will implement. Each test
// answers one of the seven hypotheses in .agent-docs/concurrent-keys/phase1-design.md §7. They are
// written as durable, executable probes of today's primitives so they survive into T2 as regression
// tests and as the executable spec for the re-anchor implementation.
//
// A written summary of the findings lives in
// .agent-docs/concurrent-keys/phase1-T0-reanchor-poc-findings.md.
//
// Terminology: "re-anchor" = swapping the single upstream SDK client when sdkKey.value changes.
// Today there is no dedicated re-anchor method; the closest existing path is ReconcileCredentials with
// an expiring (grace-period) key (which rotates the primary SDK key and stands up a new client), so
// several tests drive that path and observe where it falls short of the §7 requirements.

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/store"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reanchorTestKey2 is the "new anchor" SDK key we re-anchor onto. It is deliberately NOT in the real
// sdk-<uuid> credential format so it won't trip secret scanners; relay treats SDK keys as opaque
// non-empty strings, so any value works here.
const reanchorTestKey2 = config.SDKKey("reanchor-poc-new-anchor")

// recordingStreamUpdates is a streams.EnvStreamUpdates that counts the broadcasts it receives, so we
// can observe whether a re-anchor produces duplicate downstream "put"s.
type recordingStreamUpdates struct {
	mu             sync.Mutex
	allDataUpdates int
	singleUpdates  int
	invalidations  int
}

func (r *recordingStreamUpdates) SendAllDataUpdate(_ []ldstoretypes.Collection) {
	r.mu.Lock()
	r.allDataUpdates++
	r.mu.Unlock()
}

func (r *recordingStreamUpdates) SendSingleItemUpdate(_ ldstoretypes.DataKind, _ string, _ ldstoretypes.ItemDescriptor) {
	r.mu.Lock()
	r.singleUpdates++
	r.mu.Unlock()
}

func (r *recordingStreamUpdates) InvalidateClientSideState() {
	r.mu.Lock()
	r.invalidations++
	r.mu.Unlock()
}

func (r *recordingStreamUpdates) allDataCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allDataUpdates
}

// sharedStoreFactory is a DataStore configurer that hands back the SAME underlying store instance on
// every Build call. It models a persistent store (Redis/DynamoDB), where the data lives outside the
// process and survives the recreation of the wrapping store on a client swap.
type sharedStoreFactory struct {
	store subsystems.DataStore
}

func (f *sharedStoreFactory) Build(_ subsystems.ClientContext) (subsystems.DataStore, error) {
	return f.store, nil
}

// reanchor re-anchors env onto newKey while keeping oldKey valid for a grace hour (so the old client
// is not torn down during the swap). This mirrors the backend's default-rotation behavior: the new
// anchor is non-expiring, the demoted old anchor carries an expiry. It drives the time-injectable
// reconcileCredentials directly so the grace-period math is deterministic.
func reanchor(t *testing.T, env EnvContext, newKey, oldKey config.SDKKey, now time.Time) {
	t.Helper()
	env.(*envContextImpl).UpdateCredential(NewCredentialUpdate(newKey).
		WithGracePeriod(oldKey, now.Add(time.Hour)).
		WithTime(now))
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 1: Two SDK clients sharing a storeAdapter don't corrupt store invariants.
// -----------------------------------------------------------------------------------------------

func TestReanchorPoC_H1_SharedStoreAdapterRebuildSemantics(t *testing.T) {
	featureKind := ldstoreimpl.Features()
	flagKey := st.Flag1ServerSide.Flag.Key

	// The design (§7) assumes "two SDK clients pointed at the same env can feed the same store as a
	// side-effect." This sub-test shows that assumption is FALSE for the default in-memory store: each
	// client init calls storeAdapter.Build, which constructs a brand-new wrapper around a brand-new
	// underlying store and atomically swaps it in. No corruption occurs, but the new client starts from
	// an empty store.
	t.Run("in-memory factory builds a fresh empty store on each client init", func(t *testing.T) {
		rec := &recordingStreamUpdates{}
		adapter := store.NewSSERelayDataStoreAdapter(ldcomponents.InMemoryDataStore(), rec)

		// First client init populates the store, as the original anchor's client would.
		s1, err := adapter.Build(subsystems.BasicClientContext{})
		require.NoError(t, err)
		require.NoError(t, s1.Init(st.AllData))
		require.Same(t, s1, adapter.GetStore())

		got, err := adapter.GetStore().Get(featureKind, flagKey)
		require.NoError(t, err)
		require.NotNil(t, got.Item, "data should be present after the first client's sync")

		// Second client init = the re-anchor's "start new client" step.
		s2, err := adapter.Build(subsystems.BasicClientContext{})
		require.NoError(t, err)

		// FINDING: Build swaps in a new store instance...
		assert.NotSame(t, s1, s2, "each Build creates a new store wrapper")
		assert.Same(t, s2, adapter.GetStore(), "the adapter now points at the new store")

		// ...and that store is empty + uninitialized. So the two clients do NOT share data through the
		// in-memory store; the new anchor must re-sync from scratch.
		assert.False(t, adapter.GetStore().IsInitialized(), "the new in-memory store starts uninitialized")
		got2, err := adapter.GetStore().Get(featureKind, flagKey)
		require.NoError(t, err)
		assert.Nil(t, got2.Item, "the new in-memory store starts empty")
	})

	// With a persistent store, the underlying data lives outside the wrapper, so the swap preserves it.
	// This is the configuration in which the §7 "shared store" assumption actually holds.
	t.Run("shared (persistent) underlying store preserves data across client init", func(t *testing.T) {
		underlying, err := ldcomponents.InMemoryDataStore().Build(subsystems.BasicClientContext{})
		require.NoError(t, err)
		rec := &recordingStreamUpdates{}
		adapter := store.NewSSERelayDataStoreAdapter(&sharedStoreFactory{store: underlying}, rec)

		s1, err := adapter.Build(subsystems.BasicClientContext{})
		require.NoError(t, err)
		require.NoError(t, s1.Init(st.AllData))

		// Re-anchor's "start new client" step.
		s2, err := adapter.Build(subsystems.BasicClientContext{})
		require.NoError(t, err)

		// The wrapper is new, but it wraps the SAME underlying store, so data + initialization survive.
		assert.True(t, s2.IsInitialized(), "persistent store stays initialized across the swap")
		got, err := s2.Get(featureKind, flagKey)
		require.NoError(t, err)
		assert.NotNil(t, got.Item, "data survives the swap when the underlying store is shared")
	})
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 2: Downstream SSE connections tolerate the swap.
//   2a: an open downstream connection survives a re-anchor and keeps receiving events.
//   2b: the new anchor's initial sync re-broadcasts a (duplicate) "put" downstream.
// -----------------------------------------------------------------------------------------------

func TestReanchorPoC_H2_DownstreamConnectionSurvivesReAnchor(t *testing.T) {
	envConfig := st.EnvClientSide.Config

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	jsClientStreams := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)
	clientCh := make(chan *testclient.FakeLDClient, 10)
	sdkStartedCh := make(chan EnvContext, 10)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvClientSide.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     config.Config{},
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactoryWithChannel(true, clientCh),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		StreamProviders:  []streams.StreamProvider{jsClientStreams},
		ConnectionMapper: mockConnectionMapper{},
		Loggers:          mockLog.Loggers,
	}, sdkStartedCh)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)

	// Wait for the original anchor client and initialize the store so the client-side stream is ready.
	<-sdkStartedCh
	require.NoError(t, env.GetStore().Init(nil))

	streamHandler := env.GetStreamHandler(jsClientStreams, envConfig.EnvID)
	req, _ := http.NewRequest("GET", "", nil)
	st.WithStreamRequest(t, req, streamHandler, func(eventCh <-chan eventsource.Event) {
		initEvent := helpers.RequireValue(t, eventCh, time.Minute)
		assert.Equal(t, "ping", initEvent.Event())
		if !helpers.AssertNoMoreValues(t, eventCh, 100*time.Millisecond) {
			t.FailNow()
		}

		// --- Re-anchor while the downstream connection is open. ---
		// The connection is keyed on the env ID (a ScopedCredential), independent of the upstream SDK
		// key, so swapping the SDK anchor must not disturb it.
		start := time.Unix(1000, 0)
		reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, start)

		// The new anchor client comes up on a background goroutine. Credential additions start the client
		// with a nil readyCh (so it does NOT signal sdkStartedCh); wait on the credential set instead.
		require.Eventually(t, func() bool {
			creds := env.GetCredentials()
			for _, c := range creds {
				if c == reanchorTestKey2 {
					return true
				}
			}
			return false
		}, time.Second, 10*time.Millisecond, "rotation to the new anchor should be applied")

		// FINDING: the open client-side connection survives the swap and still delivers events.
		synchronizer.updateCh <- bigsegments.UpdatesSummary{SegmentKeysUpdated: []string{"fake-segment-key"}}
		pingEvent := helpers.RequireValue(t, eventCh, time.Second)
		assert.Equal(t, "ping", pingEvent.Event(), "downstream connection should survive the re-anchor")
	})
}

func TestReanchorPoC_H2_NewClientInitialSyncRebroadcastsPut(t *testing.T) {
	rec := &recordingStreamUpdates{}
	adapter := store.NewSSERelayDataStoreAdapter(ldcomponents.InMemoryDataStore(), rec)

	// Original anchor client builds and performs its initial sync -> one downstream "put".
	s1, err := adapter.Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	require.NoError(t, s1.Init(st.AllData))
	require.Equal(t, 1, rec.allDataCount())

	// Re-anchor: the new anchor client builds a fresh store and performs its OWN initial sync.
	s2, err := adapter.Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	require.NoError(t, s2.Init(st.AllData))

	// FINDING: the new anchor's initial sync produces a second full "put" to every connected downstream
	// stream. From a downstream SDK's perspective this is a duplicate put. It is tolerable (SDKs apply
	// puts idempotently) but T2.c must expect it; it is not a corruption.
	assert.Equal(t, 2, rec.allDataCount(), "the new anchor's initial sync re-broadcasts a full put")
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 3: Big-segment sync after re-anchor.
// -----------------------------------------------------------------------------------------------

// capturingBigSegmentSynchronizerFactory records the SDK key it was constructed with and how many
// times it was invoked, so we can detect whether a re-anchor re-wires big-segment sync.
type capturingBigSegmentSynchronizerFactory struct {
	mu           sync.Mutex
	createCount  int
	lastSDKKey   config.SDKKey
	synchronizer *mockBigSegmentSynchronizer
}

func (f *capturingBigSegmentSynchronizerFactory) create(
	_ httpconfig.HTTPConfig,
	_ bigsegments.BigSegmentStore,
	_ string,
	_ string,
	_ config.EnvironmentID,
	sdkKey config.SDKKey,
	_ ldlog.Loggers,
	_ string,
) bigsegments.BigSegmentSynchronizer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCount++
	f.lastSDKKey = sdkKey
	f.synchronizer = &mockBigSegmentSynchronizer{updateCh: make(chan bigsegments.UpdatesSummary)}
	return f.synchronizer
}

func (f *capturingBigSegmentSynchronizerFactory) snapshot() (int, config.SDKKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCount, f.lastSDKKey
}

func TestReanchorPoC_H3_BigSegmentSyncIsNotReWiredOnReAnchor(t *testing.T) {
	envConfig := st.EnvMain.Config

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	capturing := &capturingBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     config.Config{},
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: capturing.create,
		ClientFactory:                 testclient.FakeLDClientFactoryWithChannel(true, clientCh),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		ConnectionMapper: mockConnectionMapper{},
		Loggers:          mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	count, sdkKey := capturing.snapshot()
	require.Equal(t, 1, count, "the synchronizer is constructed once at env creation")
	require.Equal(t, envConfig.SDKKey, sdkKey, "it is wired to the original anchor's SDK key")

	// Re-anchor onto a new SDK key.
	start := time.Unix(1000, 0)
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, start)
	require.Eventually(t, func() bool {
		for _, c := range env.GetCredentials() {
			if c == reanchorTestKey2 {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond, "rotation to the new anchor should be applied")

	// Give any (hypothetical) re-wire a chance to run.
	require.Never(t, func() bool {
		c, _ := capturing.snapshot()
		return c != 1
	}, 200*time.Millisecond, 20*time.Millisecond, "synchronizer must not be recreated by the re-anchor")

	// FINDING: big-segment sync is wired to the SDK key at construction and is NOT re-wired by today's
	// swap path -- the synchronizer is neither recreated nor told about the new key (the
	// BigSegmentSynchronizer interface has no credential-replacement method). After re-anchor it keeps
	// polling/streaming on the OLD anchor key. T2.d must add a re-wire path (a ReplaceCredential-style
	// method) or recreate the synchronizer on each re-anchor.
	count, sdkKey = capturing.snapshot()
	assert.Equal(t, 1, count, "synchronizer was not recreated on re-anchor")
	assert.Equal(t, envConfig.SDKKey, sdkKey, "synchronizer still references the old anchor key")
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 4: httpconfig stays functional after re-anchor.
// -----------------------------------------------------------------------------------------------

func TestReanchorPoC_H4_HTTPConfigIsKeyIndependentExceptAuthHeader(t *testing.T) {
	loggers := ldlog.NewDisabledLoggers()
	key1 := config.SDKKey("sdk-key-one")
	key2 := config.SDKKey("sdk-key-two")

	var proxy config.ProxyConfig
	var httpC config.HTTPConfig

	c1, err := httpconfig.NewHTTPConfig(proxy, httpC, key1, "user-agent", loggers)
	require.NoError(t, err)
	c2, err := httpconfig.NewHTTPConfig(proxy, httpC, key2, "user-agent", loggers)
	require.NoError(t, err)

	// The ONLY key-dependent artifact is the Authorization default header on the pre-built SDK HTTP
	// config.
	assert.Equal(t, string(key1), c1.SDKHTTPConfig.DefaultHeaders.Get("Authorization"))
	assert.Equal(t, string(key2), c2.SDKHTTPConfig.DefaultHeaders.Get("Authorization"))

	// Everything else (proxy settings, user agent, and the rest of the default headers) is identical
	// and key-independent.
	h1 := c1.SDKHTTPConfig.DefaultHeaders.Clone()
	h2 := c2.SDKHTTPConfig.DefaultHeaders.Clone()
	h1.Del("Authorization")
	h2.Del("Authorization")
	assert.Equal(t, h1, h2, "non-auth default headers are key-independent")
	assert.Equal(t, c1.ProxyConfig, c2.ProxyConfig, "proxy config is key-independent")

	// FINDING: httpconfig needs NO re-wire on re-anchor. Relay injects the *builder*
	// (SDKHTTPConfigFactory) into ld.Config.HTTP, and the SDK rebuilds the HTTP config with the new
	// anchor key when it constructs the new client, so the Authorization header is set correctly for the
	// new anchor automatically. The pre-built SDKHTTPConfig / Client() (used for event + big-segment
	// transport) is key-independent except for that Authorization header, which those components set per
	// request from their own credential rather than reading it from httpconfig.
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 5: Order of operations / the in-memory store window.
// -----------------------------------------------------------------------------------------------

func TestReanchorPoC_H5_InMemoryStoreIsWipedByReAnchor(t *testing.T) {
	featureKind := ldstoreimpl.Features()
	flagKey := st.Flag1ServerSide.Flag.Key
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh), mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client1 }, time.Second, 10*time.Millisecond)

	// Populate the store as the original anchor's client would have via its stream sync.
	require.NoError(t, env.GetStore().Init(st.AllData))
	oldStore := env.GetStore()
	got, err := oldStore.Get(featureKind, flagKey)
	require.NoError(t, err)
	require.NotNil(t, got.Item)

	// Re-anchor onto a new key (old key kept valid for a grace hour, so the old client is not closed --
	// i.e. this exercises the recommended "start-new-before-close-old" ordering).
	start := time.Unix(1000, 0)
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, start)

	client2 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client2 }, time.Second, 10*time.Millisecond,
		"GetClient should return the new anchor's client once it is registered")

	// FINDING: starting the new client replaced the data store with a fresh, empty, uninitialized one.
	// This happens regardless of operation order, because building the new client is what rebuilds the
	// store. So "start-new -> swap-pointer -> close-old" alone is NOT sufficient with an in-memory store:
	// there is a window in which evaluations see an empty store until the new anchor finishes its initial
	// sync. T2.c must either (a) keep the old store/anchor authoritative until the new client reports
	// Initialized()==true, (b) require a persistent store for graceful re-anchor, or (c) decouple the
	// data store lifecycle from the client lifecycle so a new client does not rebuild it.
	newStore := env.GetStore()
	assert.NotSame(t, oldStore, newStore, "the data store instance was replaced by the new client")
	assert.False(t, newStore.IsInitialized(), "the new store is uninitialized until the new anchor re-syncs")
	got2, err := newStore.Get(featureKind, flagKey)
	require.NoError(t, err)
	assert.Nil(t, got2.Item, "data is absent in the new store until the new anchor re-syncs")
}

// TestReanchorPoC_H5_StoreHandoverAvoidsEmptyWindow validates the reviewer suggestion that, because
// relay owns the data store implementation (it hands the SDK a single storeAdapter), the re-anchor can
// hand the existing store over to the new client instead of letting it build a fresh one. Modeled here
// by a DataStoreFactory that returns the same underlying store on every Build; the production change
// (T2.c/T2.d) is to make SSERelayDataStoreAdapter reuse its store across the swap. With handover the
// new anchor's client sees the populated, initialized store immediately -- no empty-store window
// (contrast TestReanchorPoC_H5_InMemoryStoreIsWipedByReAnchor).
//
// CAVEAT for the implementation (not reproducible with the fake client, so documented here and in the
// findings): streamUpdatesStoreWrapper.Close() closes the underlying store. If the new client wraps the
// SAME underlying store, closing the retiring client must NOT close it -- the store's lifecycle has to
// be owned by the adapter, not by the client being retired.
func TestReanchorPoC_H5_StoreHandoverAvoidsEmptyWindow(t *testing.T) {
	featureKind := ldstoreimpl.Features()
	flagKey := st.Flag1ServerSide.Flag.Key
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	// A store factory that hands the same underlying store to every client (the "handover" model).
	underlying, err := ldcomponents.InMemoryDataStore().Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	handoverFactory := &sharedStoreFactory{store: underlying}

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:        envConfig,
		ClientFactory:    testclient.FakeLDClientFactoryWithChannel(true, clientCh),
		DataStoreFactory: handoverFactory,
		ConnectionMapper: mockConnectionMapper{},
		Loggers:          mockLog.Loggers,
	}, readyCh)
	require.NoError(t, err)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client1 }, time.Second, 10*time.Millisecond)

	// Populate the store as the original anchor's client would have.
	require.NoError(t, env.GetStore().Init(st.AllData))

	// Re-anchor onto a new key.
	start := time.Unix(1000, 0)
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, start)

	client2 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client2 }, time.Second, 10*time.Millisecond)

	// FINDING: with store handover there is no empty-store window -- the new client's store is still
	// initialized and still holds the data, because the underlying store was reused rather than rebuilt.
	newStore := env.GetStore()
	assert.True(t, newStore.IsInitialized(), "handed-over store stays initialized across the re-anchor")
	got, err := newStore.Get(featureKind, flagKey)
	require.NoError(t, err)
	assert.NotNil(t, got.Item, "data is preserved across the re-anchor when the store is handed over")
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 6: Behavior during the swap window (requests arriving mid-swap).
// -----------------------------------------------------------------------------------------------

func TestReanchorPoC_H6_AnchorPointerFlipsBeforeNewClientIsRegistered(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	inner := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	// gate blocks construction of the NEW anchor's client so we can observe the swap window
	// deterministically (no sleeps / no racing).
	gate := make(chan struct{})
	gatedFactory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == reanchorTestKey2 {
			<-gate
		}
		return inner(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, gatedFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client1 }, time.Second, 10*time.Millisecond)

	// Re-anchor. reconcileCredentials flips the rotator's primary SDK key synchronously, then starts the
	// new client on a background goroutine (which blocks in the factory on `gate`).
	start := time.Unix(1000, 0)
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, start)

	// FINDING: there is a window where the anchor pointer already names the new key but no client exists
	// for it yet, so GetClient() returns nil. GetClient() == clients[rotator.SDKKey()], and the rotator's
	// primary flipped to the new key before startSDKClient registered the client. A request arriving in
	// this window gets a nil client. T2.c must not advance the anchor pointer until the new client is
	// registered (and ideally Initialized()).
	assert.Nil(t, env.GetClient(), "GetClient() is nil during the swap window")

	// Release the gate; the new client registers and GetClient() recovers.
	close(gate)
	client2 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client2 }, time.Second, 10*time.Millisecond,
		"GetClient() recovers once the new client is registered")
}

// -----------------------------------------------------------------------------------------------
// Hypothesis 7: Failure modes — new client init fails.
// -----------------------------------------------------------------------------------------------

func TestReanchorPoC_H7_FailedNewClientLeavesEnvWithoutAnchorClient(t *testing.T) {
	envConfig := st.EnvMain.Config
	fakeErr := errors.New("new anchor client failed to initialize")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	inner := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	// Succeed for the original anchor; fail for the new anchor.
	failingFactory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == reanchorTestKey2 {
			return nil, fakeErr
		}
		return inner(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, failingFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client1 }, time.Second, 10*time.Millisecond)

	// Re-anchor onto a key whose client init fails (old key kept valid for a grace hour).
	start := time.Unix(1000, 0)
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, start)

	require.Eventually(t, func() bool { return env.GetInitError() != nil }, time.Second, 10*time.Millisecond,
		"the failed new-client init should surface as an init error")
	assert.Equal(t, fakeErr, env.GetInitError())

	// FINDING: the rotator already flipped the anchor to the new key, but no client exists for it, so
	// GetClient() returns nil -- even though the OLD anchor's client is still alive and valid during its
	// grace period. A failed re-anchor breaks the environment with today's code. This is exactly the §8
	// atomicity requirement: T2.c must validate that the new client initializes BEFORE swapping the
	// anchor pointer, and roll back to the old anchor on failure (preserving the previous accepted set).
	assert.Nil(t, env.GetClient(), "GetClient() is nil after a failed re-anchor")

	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.True(t, oldStillPresent,
		"the old anchor's client is still alive -- the data path could have been preserved by rolling back")
}
