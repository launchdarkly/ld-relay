package relayenv

// Regression tests for the synchronous re-anchor sequence.
//
// These cover: re-anchoring to a new key (build success and init-failure rollback), re-anchoring to a
// previously-accepted key (reuse the existing client), no orphan clients, store-handover survival, and
// the mobile-primary repoint signal (the gap when the new primary mobile key was already in the
// accepted set, so the primary-mobile gate does not fire for it).

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reanchorSyncTestKey2 = config.SDKKey("reanchor-sync-new-anchor")
const reanchorSyncExpiringKey = config.SDKKey("reanchor-sync-expiring-key")

// reanchorViaReconcile drives the production ReconcileCredentials path with a payload that
// designates newKey as the anchor and keeps oldKey accepted with an expiry one hour in the future,
// mirroring the backend's default-rotation behavior (the new anchor is non-expiring; the demoted
// old anchor carries an expiry). extraAcceptedSDK, if defined, is added as a permanent non-anchor
// SDK key — used to set up the reuse path (a key already in the accepted set later becoming the anchor).
func reanchorViaReconcile(
	t *testing.T,
	env EnvContext,
	newKey, oldKey, extraAcceptedSDK config.SDKKey,
	primaryMobile config.MobileKey,
	envID config.EnvironmentID,
	now time.Time,
) {
	t.Helper()
	builder := credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: newKey})
	if oldKey.Defined() && oldKey != newKey {
		expiry := now.Add(time.Hour)
		builder = builder.WithSDKKey(credential.SDKKeyParams{Value: oldKey, Expiry: &expiry})
	}
	if extraAcceptedSDK.Defined() && extraAcceptedSDK != newKey {
		builder = builder.WithSDKKey(credential.SDKKeyParams{Value: extraAcceptedSDK})
	}
	if primaryMobile.Defined() {
		builder = builder.WithPrimaryMobileKey(credential.MobileKeyParams{Value: primaryMobile})
	}
	if envID.Defined() {
		builder = builder.WithEnvironmentID(envID)
	}
	set, err := builder.Build()
	require.NoError(t, err)
	env.(*envContextImpl).reconcileCredentials(set, now)
}

// TestReanchorSync_CaseA_BuildsNewClientAndMovesAnchor exercises the happy path where the new anchor
// is a brand-new SDK key with no existing client: the build succeeds, and the anchor commits. The
// store is handed over (no empty-store window) and the new client becomes current.
func TestReanchorSync_CaseA_BuildsNewClientAndMovesAnchor(t *testing.T) {
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
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)

	// Populate the store as the original anchor's stream sync would.
	require.NoError(t, env.GetStore().Init(st.AllData))
	originalStore := env.GetStore()

	// Re-anchor onto a brand-new key via ReconcileCredentials.
	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

	// A new client was built synchronously and is now the current anchor's client.
	newClient := requireClientReady(t, clientCh)
	assert.NotSame(t, originalClient, newClient, "a new anchor key builds a fresh client")
	assert.Same(t, newClient, env.GetClient(), "GetClient returns the new anchor's client after the commit")
	assert.Nil(t, env.GetInitError(), "successful re-anchor clears any prior init error")

	// The rotator's primary now names the new key (CommitAnchor ran).
	assert.Equal(t, reanchorSyncTestKey2, env.(*envContextImpl).keyRotator.AnchorKey())
	assert.Contains(t, env.GetCredentials(), credential.SDKCredential(reanchorSyncTestKey2))

	// Store handover: the wrapper is the same instance, still initialized, data intact.
	assert.Same(t, originalStore, env.GetStore(), "store handover: the wrapper survives the re-anchor")
	got, err := env.GetStore().Get(featureKind, flagKey)
	require.NoError(t, err)
	assert.NotNil(t, got.Item, "data survives the re-anchor (no empty-store window)")

	// The old client is still alive — its grace period has not elapsed (the old client keeps serving;
	// closure happens via removeCredential when the expiry fires).
	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.True(t, oldStillPresent, "old anchor's client retained during its grace period")
}

// TestReanchorSync_CaseA_InitFailureRollsBack confirms that a failed new-client init does NOT move
// the rotator's anchor, does NOT install the broken client, and does NOT close the previous client.
// The old anchor keeps serving; the failure surfaces as a structured Errorf log (initErr is left
// untouched so the still-healthy env is not marked failed). This is the all-or-nothing atomicity
// requirement applied to re-anchor.
func TestReanchorSync_CaseA_InitFailureRollsBack(t *testing.T) {
	envConfig := st.EnvMain.Config
	fakeErr := errors.New("re-anchor: new client init refused")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthyFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	// Fail only for the new anchor key; succeed for the original.
	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == reanchorSyncTestKey2 {
			return nil, fakeErr
		}
		return healthyFactory(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

	// Rollback: the env stays healthy on the previous anchor, so GetInitError stays nil — setting it
	// would 401 a still-serving env at the request middleware. The failure surfaces via a structured
	// Error log instead. Anchor pointer unchanged, no new client installed, old client still in place.
	assert.NoError(t, env.GetInitError(), "a failed re-anchor must not mark the still-serving env as failed")
	mockLog.AssertMessageMatch(t, true, ldlog.Error, "Re-anchor to SDK key .* failed")
	assert.Same(t, originalClient, env.GetClient(), "GetClient still returns the previous anchor's client")
	assert.Equal(t, envConfig.SDKKey, env.(*envContextImpl).keyRotator.AnchorKey(), "anchor pointer stayed on the previous key")

	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, newAnchorClientInstalled := envImpl.clients[reanchorSyncTestKey2]
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.False(t, newAnchorClientInstalled, "no client installed for the failed new anchor")
	assert.True(t, oldStillPresent, "old anchor's client preserved on rollback")
}

// TestReanchorSync_CaseB_ReusesExistingClient covers re-anchoring onto a key that already has a
// live client. The simplest deterministic setup: re-anchor A→B (B's client built), then re-anchor
// B→A while A is still in its grace period. A's client still exists, so the second re-anchor must
// reuse it and build nothing new.
func TestReanchorSync_CaseB_ReusesExistingClient(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh), mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)
	require.NoError(t, env.GetStore().Init(st.AllData))

	// First re-anchor: original → key2 (new key; key2 client built). Keep the original in grace.
	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	key2Client := requireClientReady(t, clientCh)
	require.Same(t, key2Client, env.GetClient())

	// The original anchor's client is still alive in its grace period.
	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	originalStillPresent := envImpl.clients[envConfig.SDKKey] == originalClient
	envImpl.mu.RUnlock()
	require.True(t, originalStillPresent, "original client retained for reuse")

	// Second re-anchor: key2 → original. The original's client exists, so this is the reuse path: no
	// Build, the existing client is reused, the anchor flips, and ReplaceCredential runs.
	reanchorViaReconcile(t, env, envConfig.SDKKey, reanchorSyncTestKey2, "", envConfig.MobileKey, envConfig.EnvID, now)

	// No new client was created — clientCh must be empty (every prior client was drained).
	select {
	case c := <-clientCh:
		t.Fatalf("re-anchoring to a key with an existing client must not build a new one, but one was created: %v", c.Key)
	case <-time.After(100 * time.Millisecond):
	}

	assert.Same(t, originalClient, env.GetClient(), "reuses the existing client for the re-anchored key")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "anchor flipped back to the original key")
}

// TestReanchorSync_CredentialExpiryDuringReanchorIsSerialized exercises the concurrency gap closed
// by serializing the cleanup ticker against reconcileMu: a credential expiry firing (via the ticker
// path, triggerCredentialChanges) while a synchronous re-anchor is mid-build must not run its
// StepTime + add/remove pass concurrently with the in-flight re-anchor. Both paths drain the same
// StepTime queue and can close clients, so they must be serialized the way concurrent reconciles are.
//
// The test wedges a re-anchor open by blocking the new anchor's client build, then fires the ticker
// from another goroutine and asserts (a) the ticker is blocked while the re-anchor holds reconcileMu,
// (b) it completes once the re-anchor releases it, and (c) the final state is consistent — the new
// anchor committed, the expiring non-anchor key dropped, the demoted old anchor retained in grace.
func TestReanchorSync_CredentialExpiryDuringReanchorIsSerialized(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	now := time.Unix(2000, 0)
	expiringExpiry := now.Add(30 * time.Minute) // the non-anchor key the ticker will drop
	graceExpiry := now.Add(2 * time.Hour)       // the demoted old anchor stays alive in its grace period
	tickerTime := now.Add(time.Hour)            // between the two expiries: drops only the expiring key

	// The new anchor's client build blocks until releaseBuild is closed, holding the re-anchor (and
	// thus reconcileMu) open so the ticker has a window to (try to) run concurrently.
	buildEntered := make(chan struct{})
	releaseBuild := make(chan struct{})

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthy := testclient.FakeLDClientFactoryWithChannel(true, clientCh)
	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == reanchorSyncTestKey2 {
			close(buildEntered)
			<-releaseBuild
		}
		return healthy(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)
	require.NoError(t, env.GetStore().Init(st.AllData))

	envImpl := env.(*envContextImpl)

	// Accept a second SDK key as a non-anchor server key carrying a future expiry. addCredential's
	// anchor gate won't build a client for it, so it has no client of its own.
	initialSet, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
		WithSDKKey(credential.SDKKeyParams{Value: reanchorSyncExpiringKey, Expiry: &expiringExpiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(initialSet, now)
	require.Contains(t, env.GetCredentials(), credential.SDKCredential(reanchorSyncExpiringKey))

	// Re-anchor onto a brand-new key while keeping both the demoted old anchor and the expiring key
	// accepted. The build will block, holding reconcileMu.
	reanchorSet, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: reanchorSyncTestKey2}).
		WithSDKKey(credential.SDKKeyParams{Value: envConfig.SDKKey, Expiry: &graceExpiry}).
		WithSDKKey(credential.SDKKeyParams{Value: reanchorSyncExpiringKey, Expiry: &expiringExpiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)

	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		envImpl.reconcileCredentials(reanchorSet, now)
	}()

	// Wait until the re-anchor is wedged open inside the client build (holding reconcileMu).
	<-buildEntered

	// Fire the cleanup ticker's work while the re-anchor holds reconcileMu. With the ticker serialized
	// against reconcileMu, this must block until the re-anchor releases it.
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		envImpl.triggerCredentialChanges(tickerTime)
	}()

	select {
	case <-tickerDone:
		t.Fatal("the cleanup ticker ran its StepTime + add/remove pass concurrently with an in-flight " +
			"re-anchor; reconcileMu did not serialize the ticker against reconcileCredentials")
	case <-time.After(100 * time.Millisecond):
		// Expected: the ticker is blocked on reconcileMu.
	}

	// Release the build; the re-anchor finishes and drops reconcileMu, unblocking the ticker.
	close(releaseBuild)
	<-reconcileDone
	select {
	case <-tickerDone:
	case <-time.After(time.Second):
		t.Fatal("the cleanup ticker did not complete after the re-anchor released reconcileMu")
	}

	// Final state is consistent: the new anchor committed and serves its client, the expiring
	// non-anchor key was dropped by the ticker, and the demoted old anchor is retained in grace.
	newClient := requireClientReady(t, clientCh)
	assert.Same(t, newClient, env.GetClient(), "the new anchor's client is current after the re-anchor")
	assert.Equal(t, reanchorSyncTestKey2, envImpl.keyRotator.AnchorKey())
	assert.NotContains(t, env.GetCredentials(), credential.SDKCredential(reanchorSyncExpiringKey),
		"the expiring non-anchor key was dropped by the ticker")

	envImpl.mu.RLock()
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.True(t, oldStillPresent, "demoted old anchor's client retained during its grace period")
}

// TestReanchorSync_MobilePrimaryRepoint_AlreadyAcceptedKey covers the primary-mobile gate's gap:
// when the primary mobile key switches to a key that was ALREADY in the accepted set, that key is
// not in StepTime's additions, so addCredential's gate never fires for it. reconcileCredentials
// must therefore call eventDispatcher.ReplaceCredential synchronously via MobilePrimaryRepoint.
//
// This test asserts the rotator-level signal: after reconciling to make an already-accepted mobile
// key the primary, the env's primary mobile key reflects the change. (The ReplaceCredential side
// effect on the dispatcher is exercised by the events package's own dispatcher tests; here we
// confirm the env drives the primary-mobile transition correctly without spawning a client.)
func TestReanchorSync_MobilePrimaryRepoint_AlreadyAcceptedKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	mob1 := config.MobileKey("mob-primary-1")
	mob2 := config.MobileKey("mob-primary-2")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh), mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	_ = requireClientReady(t, clientCh)

	now := time.Unix(2000, 0)
	envImpl := env.(*envContextImpl)

	// Accept both mobile keys, mob1 primary. mob2 is accepted but not primary.
	set1, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: mob1}).
		WithMobileKey(credential.MobileKeyParams{Value: mob2}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set1, now)
	require.Equal(t, mob1, envImpl.keyRotator.MobileKey())
	require.Contains(t, env.GetCredentials(), credential.SDKCredential(mob2))

	// Now make mob2 (already accepted) the primary mobile key. No SDK anchor change here.
	set2, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: mob2}).
		WithMobileKey(credential.MobileKeyParams{Value: mob1}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set2, now)

	assert.Equal(t, mob2, envImpl.keyRotator.MobileKey(), "primary mobile key repointed to the already-accepted key")
	// No SDK client should have been spawned by a mobile-only change.
	select {
	case c := <-clientCh:
		t.Fatalf("a mobile-primary repoint must not build an SDK client, got: %v", c.Key)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestReanchorSync_PreviouslyAcceptedNonAnchorPromotedToAnchor covers the case the two-signal split in
// reanchor exists for: a server SDK key accepted as a NON-anchor has its credential mappings registered
// by addCredential but no client (only the anchor gets one). Promoting it to anchor must NOT re-register
// its mappings (NewAnchorPreviouslyAccepted == true) but MUST build a client (existingClient == nil).
func TestReanchorSync_PreviouslyAcceptedNonAnchorPromotedToAnchor(t *testing.T) {
	envConfig := st.EnvMain.Config
	nonAnchorKey := config.SDKKey("reanchor-sync-nonanchor")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh), mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)
	require.NoError(t, env.GetStore().Init(st.AllData))

	now := time.Unix(2000, 0)
	envImpl := env.(*envContextImpl)

	// Accept a second server SDK key as a NON-anchor (permanent). Its mappings are registered but no
	// client is built — only the anchor owns an upstream client.
	set1, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
		WithSDKKey(credential.SDKKeyParams{Value: nonAnchorKey}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set1, now)
	require.Contains(t, env.GetCredentials(), credential.SDKCredential(nonAnchorKey))
	select {
	case c := <-clientCh:
		t.Fatalf("a non-anchor server key must not build a client, got: %v", c.Key)
	case <-time.After(100 * time.Millisecond):
	}
	envImpl.mu.RLock()
	_, nonAnchorHasClient := envImpl.clients[nonAnchorKey]
	envImpl.mu.RUnlock()
	require.False(t, nonAnchorHasClient, "non-anchor key has no client of its own")

	// Promote the previously-accepted non-anchor key to anchor: mappings already exist (skip
	// re-registration), but there is no client, so the re-anchor must build one.
	reanchorViaReconcile(t, env, nonAnchorKey, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

	newClient := requireClientReady(t, clientCh)
	assert.NotSame(t, originalClient, newClient, "promoting a client-less accepted key builds a fresh client")
	assert.Same(t, newClient, env.GetClient())
	assert.Equal(t, nonAnchorKey, envImpl.keyRotator.AnchorKey(), "anchor committed to the promoted key")
	assert.NoError(t, env.GetInitError())
}

// TestReanchorSync_Offline_CommitsWithoutBuildingClient covers the offline re-anchor branch: when the
// env is offline, re-anchoring to a new key must commit the anchor WITHOUT building a new upstream
// client. (The initial anchor client is still created at startup; offline only skips the re-anchor
// build.)
func TestReanchorSync_Offline_CommitsWithoutBuildingClient(t *testing.T) {
	envConfig := st.EnvMain.Config
	envConfig.Offline = true

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh), mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	_ = requireClientReady(t, clientCh) // drain the initial anchor client

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

	// The anchor commits, but the offline branch builds no new client.
	assert.Equal(t, reanchorSyncTestKey2, env.(*envContextImpl).keyRotator.AnchorKey(), "offline re-anchor commits the anchor")
	select {
	case c := <-clientCh:
		t.Fatalf("an offline re-anchor must not build a new SDK client, got: %v", c.Key)
	case <-time.After(100 * time.Millisecond):
	}
	assert.NoError(t, env.GetInitError())
}
