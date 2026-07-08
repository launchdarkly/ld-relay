package relayenv

// Regression (multi-agent review, PR #739): a stale initial build must not clobber initErr after an
// A->B->A re-anchor.
//
// The initial startSDKClient(A) hangs, the anchor rotates A->B (healthy, initErr=nil), then B->A (a
// fresh healthy A client is built and committed, initErr=nil). When the ORIGINAL hung A build finally
// returns ErrInitializationFailed, it is no longer the client backing the anchor, so it must leave
// initErr untouched -- otherwise the middleware would 401 a healthy env. A gate of key-equality alone
// (sdkKey == AnchorKey()) is not enough here; the fix also requires this build to be the anchor's
// installed client.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReanchor_DoubleReanchorBackDoesNotClobberInitErr(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthy := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	var aCalls int32
	gate := make(chan struct{}) // releases the hung initial A build
	enteredFirstA := make(chan struct{}, 1)

	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == envConfig.SDKKey { // anchor A
			if atomic.AddInt32(&aCalls, 1) == 1 {
				// The ORIGINAL initial build: hang, then fail late -- returning a NON-NIL, uninitialized
				// client with the error, as the real SDK does. If it were installed it would close the
				// fresh healthy A client from the B->A rebuild and swap in this dead one.
				enteredFirstA <- struct{}{}
				<-gate
				return &testclient.FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{})}, ld.ErrInitializationFailed
			}
			// The B->A rebuild: healthy.
			return healthy(sdkKey, cfg, timeout)
		}
		return healthy(sdkKey, cfg, timeout) // B healthy
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	<-enteredFirstA // initial A build is hung; anchor is A, no client installed yet

	now := time.Unix(2000, 0)
	expiry := now.Add(time.Hour)

	// A -> B (B brand new/healthy; A grace-demoted).
	setB, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: reanchorSyncTestKey2}).
		WithSDKKey(credential.SDKKeyParams{Value: envConfig.SDKKey, Expiry: &expiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(setB, now)

	// B -> A (A still in grace, no client -> reanchor builds a fresh healthy A client and commits).
	setA, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
		WithSDKKey(credential.SDKKeyParams{Value: reanchorSyncTestKey2, Expiry: &expiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: envConfig.MobileKey}).
		WithEnvironmentID(envConfig.EnvID).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(setA, now)

	// Sanity: A is the anchor again, on a fresh healthy client, env healthy.
	require.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey())
	require.NoError(t, env.GetInitError(), "sanity: healthy after re-anchoring back to A")
	freshA := env.GetClient()
	require.NotNil(t, freshA)
	require.True(t, freshA.Initialized())

	// The ORIGINAL hung A build now fails late.
	close(gate)
	<-readyCh

	// It must NOT clobber the healthy env's initErr.
	assert.NoError(t, env.GetInitError(),
		"a stale initial build failure must not clobber a healthy re-anchored-back env")
	assert.NotEqual(t, ld.ErrInitializationFailed, env.GetInitError(),
		"the middleware would 401 the whole healthy env on ErrInitializationFailed")
	// The fresh healthy client from the B->A rebuild must still be the anchor's client -- the stale
	// build must not have closed it and swapped in its dead, uninitialized client.
	assert.Same(t, freshA, env.GetClient(), "the stale build must not replace the fresh healthy client")
	assert.True(t, env.GetClient().Initialized(), "the anchor client is still the initialized one")
}
