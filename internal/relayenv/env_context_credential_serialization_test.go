package relayenv

// Regression for the deferred-flip concurrency hazard: the cleanup ticker's triggerCredentialChanges
// must be serialized against reconcileCredentials via reconcileMu. Because Reconcile queues the new
// anchor's addition but defers the pointer flip to CommitAnchor, a ticker that drained that addition in
// the window between them would run addCredential with the anchor still on the old key, skip the new
// anchor's startSDKClient, and leave the env with no upstream client. reconcileMu closes that window.

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

	// Stand in for an in-flight reconcileCredentials by holding reconcileMu: while it is held, the
	// cleanup ticker's triggerCredentialChanges must NOT run (that is exactly the interleaving that would
	// steal a queued addition mid-re-anchor).
	envImpl.reconcileMu.Lock()

	tickerDone := make(chan struct{})
	go func() {
		envImpl.triggerCredentialChanges(time.Unix(3000, 0))
		close(tickerDone)
	}()

	select {
	case <-tickerDone:
		envImpl.reconcileMu.Unlock()
		t.Fatal("triggerCredentialChanges ran while reconcileMu was held: the ticker is not serialized against reconcile")
	case <-time.After(100 * time.Millisecond):
		// Expected: the ticker is blocked on reconcileMu.
	}

	// Once the "reconcile" releases the lock, the ticker proceeds.
	envImpl.reconcileMu.Unlock()
	select {
	case <-tickerDone:
	case <-time.After(time.Second):
		t.Fatal("triggerCredentialChanges did not proceed after reconcileMu was released")
	}
}
