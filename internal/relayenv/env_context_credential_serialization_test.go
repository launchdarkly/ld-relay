package relayenv

// Regression for the deferred-flip concurrency hazard: the cleanup ticker's triggerCredentialChanges
// must be serialized against reconcileCredentials via reconcileSem. Because Reconcile queues the new
// anchor's addition but defers the pointer flip to CommitAnchor, a ticker that drained that addition in
// the window between them would run addCredential with the anchor still on the old key, skip the new
// anchor's startSDKClient, and leave the env with no upstream client. reconcileSem closes that window.

import (
	"testing"
	"time"

	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/require"
)

func TestCredentialTickerIsSerializedAgainstReconcile(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh), mockLog.Loggers, readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	requireClientReady(t, clientCh)

	envImpl := env.(*envContextImpl)

	// Stand in for an in-flight reconcileCredentials by holding reconcileSem: while it is held, the
	// cleanup ticker's triggerCredentialChanges must NOT run (that is exactly the interleaving that would
	// steal a queued addition mid-re-anchor).
	envImpl.acquireReconcile()

	tickerDone := make(chan struct{})
	go func() {
		envImpl.triggerCredentialChanges(time.Unix(3000, 0))
		close(tickerDone)
	}()

	select {
	case <-tickerDone:
		envImpl.releaseReconcile()
		t.Fatal("triggerCredentialChanges ran while reconcileSem was held: the ticker is not serialized against reconcile")
	case <-time.After(100 * time.Millisecond):
		// Expected: the ticker is blocked acquiring reconcileSem.
	}

	// Once the "reconcile" releases it, the ticker proceeds.
	envImpl.releaseReconcile()
	select {
	case <-tickerDone:
	case <-time.After(time.Second):
		t.Fatal("triggerCredentialChanges did not proceed after reconcileSem was released")
	}
}
