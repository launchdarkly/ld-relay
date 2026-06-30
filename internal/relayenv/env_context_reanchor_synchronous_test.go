package relayenv

// Regression tests for the synchronous re-anchor sequence (T2.c / SDK-2542).
//
// These cover the acceptance criteria from .agent-docs/concurrent-keys/phase1-design.md §7:
// Case A success, Case A init-failure rollback, Case B (reused client), no orphan clients,
// store-handover survival, and the mobile-primary repoint signal (the gap left by PR #712's gate
// when the new primary mobile key was already in the accepted set).

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reanchorSyncTestKey2 = config.SDKKey("reanchor-sync-new-anchor")

// reanchorViaReconcile drives the production ReconcileCredentials path with a payload that
// designates newKey as the anchor and keeps oldKey accepted with an expiry one hour in the future,
// mirroring the backend's default-rotation behavior (the new anchor is non-expiring; the demoted
// old anchor carries an expiry). extraAcceptedSDK, if defined, is added as a permanent non-anchor
// SDK key — used to set up Case B (a key already in the accepted set later becoming the anchor).
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

// TestReanchorSync_CaseA_BuildsNewClientAndMovesAnchor exercises the happy-path Case A re-anchor:
// the new anchor is a brand-new SDK key with no existing client, the build succeeds, and the anchor
// commits. The store is handed over (no empty-store window) and the new client becomes current.
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
	assert.NotSame(t, originalClient, newClient, "Case A builds a fresh client for the new anchor")
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

	// The old client is still alive — its grace period has not elapsed (PoC H2's "old client keeps
	// serving" property; closure happens via removeCredential when the expiry fires).
	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	_, oldStillPresent := envImpl.clients[envConfig.SDKKey]
	envImpl.mu.RUnlock()
	assert.True(t, oldStillPresent, "old anchor's client retained during its grace period")
}

// TestReanchorSync_CaseA_InitFailureRollsBack confirms that a failed new-client init does NOT move
// the rotator's anchor, does NOT install the broken client, and does NOT close the previous client.
// The old anchor keeps serving; the failure surfaces as initErr + a structured Errorf log. This is
// the §8 atomicity requirement applied to re-anchor (PoC H7 baseline, now fixed).
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

	// Rollback: anchor pointer unchanged, no new client installed, old client still in place.
	require.Equal(t, fakeErr, env.GetInitError(), "init failure surfaces on the env")
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
// live client. We first accept a second SDK key as a non-anchor server key (which, being the
// anchor of its own reconcile, would build a client)... but the simplest deterministic Case B is:
// re-anchor A→B (B's client built), then re-anchor B→A while A is still in its grace period. A's
// client still exists, so the second re-anchor must reuse it and build nothing new.
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

	// First re-anchor: original → key2 (Case A; key2 client built). Keep the original in grace.
	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	key2Client := requireClientReady(t, clientCh)
	require.Same(t, key2Client, env.GetClient())

	// The original anchor's client is still alive in its grace period.
	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	originalStillPresent := envImpl.clients[envConfig.SDKKey] == originalClient
	envImpl.mu.RUnlock()
	require.True(t, originalStillPresent, "original client retained for Case B reuse")

	// Second re-anchor: key2 → original. The original's client exists, so this is Case B: no Build,
	// the existing client is reused, the anchor flips, and ReplaceCredential runs.
	reanchorViaReconcile(t, env, envConfig.SDKKey, reanchorSyncTestKey2, "", envConfig.MobileKey, envConfig.EnvID, now)

	// No new client was created — clientCh must be empty (every prior client was drained).
	select {
	case c := <-clientCh:
		t.Fatalf("Case B must not build a new client, but one was created: %v", c.Key)
	case <-time.After(100 * time.Millisecond):
	}

	assert.Same(t, originalClient, env.GetClient(), "Case B reuses the existing client for the re-anchored key")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "anchor flipped back to the original key")
}

// TestReanchorSync_MobilePrimaryRepoint_AlreadyAcceptedKey covers the gap left by PR #712's gate:
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
