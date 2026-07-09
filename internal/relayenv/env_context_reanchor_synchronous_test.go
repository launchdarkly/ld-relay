package relayenv

// Regression tests for the synchronous re-anchor sequence.
//
// These cover: re-anchoring to a new key (build success and init-failure rollback), re-anchoring back
// to a previously-accepted key (fresh client build -- the demoted key's client was closed when its
// demotion committed), no orphan clients, store-handover survival, and the mobile-primary repoint
// signal (the gap when the new primary mobile key was already in the accepted set, so the
// primary-mobile gate does not fire for it).

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
	helpers "github.com/launchdarkly/go-test-helpers/v3"

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

	// The demoted old anchor's client is closed as part of the commit: the anchor owns the env's
	// single upstream connection, and a second live stream would feed the shared store wrapper and
	// broadcast every update twice to connected clients. Only the client goes -- the key itself stays
	// accepted, so it keeps authenticating downstream connections during its grace period.
	originalClient.AwaitClose(t, time.Second)
	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.False(t, oldStillPresent, "demoted old anchor's client is removed at commit")
	assert.Contains(t, env.GetCredentials(), credential.SDKCredential(envConfig.SDKKey),
		"the demoted key remains accepted for downstream auth during its grace period")
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

// TestReanchorSync_CaseB_RepromoteInGraceKeyBuildsFreshClient covers re-anchoring back onto a
// previously-accepted key: re-anchor A→B (B's client built, A's client closed at commit), then
// re-anchor B→A while A is still in its grace period. A's credential mappings survived the
// demotion, but its client did not, so the second re-anchor must build a fresh client for A and
// close B's client at commit.
func TestReanchorSync_CaseB_RepromoteInGraceKeyBuildsFreshClient(t *testing.T) {
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

	// The original anchor's client was closed at commit; only its credential mappings survive.
	originalClient.AwaitClose(t, time.Second)
	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, originalStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	require.False(t, originalStillPresent, "demoted original anchor's client removed at commit")

	// Second re-anchor: key2 → original. The original key is still accepted (its mappings were never
	// torn down) but it has no client, so a fresh one is built; key2's client closes at commit.
	reanchorViaReconcile(t, env, envConfig.SDKKey, reanchorSyncTestKey2, "", envConfig.MobileKey, envConfig.EnvID, now)

	freshClient := requireClientReady(t, clientCh)
	assert.NotSame(t, originalClient, freshClient, "re-promoting an in-grace key builds a fresh client")
	assert.Same(t, freshClient, env.GetClient(), "the fresh client is current after the re-anchor")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "anchor flipped back to the original key")
	key2Client.AwaitClose(t, time.Second)
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
// anchor committed, the expiring non-anchor key dropped, the demoted old anchor still accepted in
// grace (though its client was closed when the re-anchor committed).
func TestReanchorSync_CredentialExpiryDuringReanchorIsSerialized(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	now := time.Unix(2000, 0)
	expiringExpiry := now.Add(30 * time.Minute) // the non-anchor key the ticker will drop
	graceExpiry := now.Add(2 * time.Hour)       // the demoted old anchor stays accepted in its grace period
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

	originalClient.AwaitClose(t, time.Second)
	envImpl.mu.RLock()
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.False(t, oldStillPresent, "demoted old anchor's client closed when the re-anchor committed")
	assert.Contains(t, env.GetCredentials(), credential.SDKCredential(envConfig.SDKKey),
		"demoted old anchor remains accepted for downstream auth during its grace period")
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
// client, and the environment's single file-data client must keep serving. (The initial anchor
// client is still created at startup; offline skips both the re-anchor build and the demoted-anchor
// client teardown -- closing the only client with no replacement would flip /status to disconnected
// and, with a persistent store, tear down the backing store.)
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
	initialClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == initialClient }, time.Second, 10*time.Millisecond)

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

	// The single offline client survives the rotation: it is not closed and GetClient still finds it.
	if !helpers.AssertChannelNotClosed(t, initialClient.CloseCh, 100*time.Millisecond,
		"the offline env's only client must not be closed by a re-anchor") {
		t.FailNow()
	}
	assert.Same(t, initialClient, env.GetClient(), "GetClient keeps returning the offline client after re-anchor")
}

// TestReanchorSync_RollbackWithImmediateRevocationKeepsOldAnchorServing covers the edge where a
// reconcile both moves the anchor to a new key AND immediately revokes the current anchor (no grace
// expiry), and the new anchor's client fails to build. The re-anchor rolls back, backing out just the
// anchor change: the previous anchor is kept serving and re-admitted to the accepted set, and the
// failed new key is dropped.
func TestReanchorSync_RollbackWithImmediateRevocationKeepsOldAnchorServing(t *testing.T) {
	envConfig := st.EnvMain.Config
	fakeErr := errors.New("re-anchor: new client init refused")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthyFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)
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
	require.NoError(t, env.GetStore().Init(st.AllData))

	envImpl := env.(*envContextImpl)
	now := time.Unix(2000, 0)

	// Re-anchor to a brand-new key that fails to init, while immediately revoking the current anchor —
	// it is omitted from the payload entirely (no grace expiry).
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: reanchorSyncTestKey2}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set, now)

	// The rollback backed out the anchor change: the previous anchor still serves and is still accepted,
	// the failed new key is gone, and the env is not marked failed.
	assert.NoError(t, env.GetInitError(), "a rolled-back re-anchor must not mark the env failed")
	assert.Same(t, originalClient, env.GetClient(), "the previous anchor's client keeps serving despite the revocation")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "anchor pointer stayed on the previous key")
	assert.Contains(t, env.GetCredentials(), credential.SDKCredential(envConfig.SDKKey), "previous anchor re-admitted to the accepted set")
	assert.NotContains(t, env.GetCredentials(), credential.SDKCredential(reanchorSyncTestKey2), "failed new anchor dropped from the accepted set")

	envImpl.mu.RLock()
	_, newHasClient := envImpl.clients[reanchorSyncTestKey2]
	_, oldHasClient := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.False(t, newHasClient, "no client for the failed new anchor")
	assert.True(t, oldHasClient, "previous anchor's client retained")

	// A later cleanup ticker must not reap the re-admitted anchor: RevertAnchorChange re-admits it as a
	// permanent (nil-expiry) key, so there is nothing to expire. (Complements the grace-demotion case,
	// where the anchor keeps a stale expiry and the StepTime guard is what protects it.)
	envImpl.triggerCredentialChanges(now.Add(2 * time.Hour))
	assert.Same(t, originalClient, env.GetClient(), "the re-admitted anchor still serves after a later ticker")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey())
}

// TestReanchorSync_PreviouslyAcceptedAnchorPromotionFailureKeepsItsMappings covers a failed promotion
// of an already-accepted non-anchor key: the rollback must NOT tear down that key's credential
// mappings (they predate this reconcile), since RevertAnchorChange keeps it accepted. It should revert
// cleanly to the non-anchor key it already was.
func TestReanchorSync_PreviouslyAcceptedAnchorPromotionFailureKeepsItsMappings(t *testing.T) {
	envConfig := st.EnvMain.Config
	nonAnchorKey := config.SDKKey("reanchor-sync-nonanchor-promote-fail")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthyFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)
	// Fail the build only when nonAnchorKey is promoted to anchor.
	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == nonAnchorKey {
			return nil, errors.New("promotion build refused")
		}
		return healthyFactory(sdkKey, cfg, timeout)
	}

	mapper := &recordingConnectionMapper{}
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnvWithMapper(t, envConfig, factory, mockLog.Loggers, readyCh, mapper)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)
	require.NoError(t, env.GetStore().Init(st.AllData))

	envImpl := env.(*envContextImpl)
	now := time.Unix(2000, 0)

	// Accept nonAnchorKey as a non-anchor server key: mappings are registered (and it becomes mapped),
	// but no client is built for it.
	set1, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
		WithSDKKey(credential.SDKKeyParams{Value: nonAnchorKey}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set1, now)
	require.True(t, mapper.isMapped(envConfig.FilterKey, nonAnchorKey), "non-anchor key is mapped once accepted")

	// Promote nonAnchorKey to anchor; its client build fails, so the re-anchor rolls back.
	reanchorViaReconcile(t, env, nonAnchorKey, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

	// The failed promotion rolled back: nonAnchorKey stays accepted AND keeps its connection mapping —
	// the rollback must not strip mappings that existed before this reconcile.
	assert.Contains(t, env.GetCredentials(), credential.SDKCredential(nonAnchorKey), "previously-accepted key stays accepted after a failed promotion")
	assert.True(t, mapper.isMapped(envConfig.FilterKey, nonAnchorKey), "its connection mapping must survive the rollback")
	// The previous anchor still serves.
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey())
	assert.Same(t, originalClient, env.GetClient())
	assert.NoError(t, env.GetInitError())
}

// TestReanchor_SupersededFailingBuildDoesNotClobberInitErr: the initial startSDKClient(A) is still in
// flight when a re-anchor to healthy B commits (initErr=nil). A's build then fails -- returning a
// NON-NIL, uninitialized client with the error, exactly as the real SDK's MakeCustomClient does. Because
// a re-anchor committed since this build launched, it is superseded: it must be discarded, not installed,
// and must not touch initErr -- otherwise it would clobber a healthy env into a whole-env 401.
func TestReanchor_SupersededFailingBuildDoesNotClobberInitErr(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthy := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == envConfig.SDKKey {
			// The ORIGINAL anchor A: block, then fail like the real SDK -- a non-nil, uninitialized client
			// plus the error (MakeCustomClient returns the client on init failure/timeout).
			entered <- struct{}{}
			<-gate
			return &testclient.FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{})}, ld.ErrInitializationFailed
		}
		return healthy(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()

	envImpl := env.(*envContextImpl)
	<-entered // A's initial build is blocked; anchor is still A.

	// Re-anchor A -> B (B brand new/healthy; A grace-demoted +1h). B builds, commits, clears initErr.
	now := time.Unix(2000, 0)
	expiry := now.Add(time.Hour)
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: reanchorSyncTestKey2}).
		WithSDKKey(credential.SDKKeyParams{Value: envConfig.SDKKey, Expiry: &expiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set, now)
	require.Equal(t, reanchorSyncTestKey2, envImpl.keyRotator.AnchorKey())
	require.NoError(t, env.GetInitError(), "sanity: healthy after re-anchor to B")

	// The stale initial A build now fails late.
	close(gate)
	<-readyCh

	assert.NoError(t, env.GetInitError(),
		"a superseded build's late failure must not clobber the healthy env's initErr")
	assert.NotEqual(t, ld.ErrInitializationFailed, env.GetInitError())
	assert.NotNil(t, env.GetClient(), "B's healthy client still serves")
	// The superseded build was discarded, not installed for A.
	envImpl.mu.RLock()
	_, aHasClient := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.False(t, aHasClient, "the superseded build must not be installed for A")
}

// TestReanchor_SupersededLateBuildIsDiscarded: even a *successful* initial build is discarded if a
// re-anchor committed a fresh anchor client while it was in flight. Installing it would tear down the
// current anchor client (startSDKClient's stale-client guard closes whatever is installed) and swap in
// an obsolete one. The superseded build must be dropped, leaving the committed anchor untouched.
func TestReanchor_SupersededLateBuildIsDiscarded(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthy := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == envConfig.SDKKey {
			// The ORIGINAL anchor A: block, then build SUCCESSFULLY (late).
			entered <- struct{}{}
			<-gate
		}
		return healthy(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()

	envImpl := env.(*envContextImpl)
	<-entered

	now := time.Unix(2000, 0)
	expiry := now.Add(time.Hour)
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: reanchorSyncTestKey2}).
		WithSDKKey(credential.SDKKeyParams{Value: envConfig.SDKKey, Expiry: &expiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(set, now)
	require.Equal(t, reanchorSyncTestKey2, envImpl.keyRotator.AnchorKey())
	bClient := env.GetClient()
	require.NotNil(t, bClient)

	// A's initial build completes successfully, after the re-anchor -- but it is superseded.
	close(gate)
	<-readyCh

	// It was discarded: A has no client, and B's client is untouched.
	envImpl.mu.RLock()
	_, aHasClient := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.False(t, aHasClient, "a superseded successful build must be discarded, not installed for A")
	assert.Same(t, bClient, env.GetClient(), "B's committed client must not be torn down by the stale build")
	assert.Equal(t, reanchorSyncTestKey2, envImpl.keyRotator.AnchorKey(), "anchor stays B")
	assert.NoError(t, env.GetInitError())
}
