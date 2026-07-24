package relayenv

// Regression test for tearing down an environment while a re-anchor's client build is in flight. The
// re-anchor releases c.mu around the (potentially slow, SDK-init-timeout-bounded) client build, so a
// concurrent Close() can run its teardown during that window. When the build then completes, the
// re-anchor must observe that the env is closed, discard and close the freshly-built client rather than
// installing it, and leave no client behind.

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reanchorCloseNewAnchor = config.SDKKey("reanchor-close-new-anchor")

// TestReanchorClosedDuringInFlightBuild wedges the new anchor's synchronous client build, calls Close()
// from another goroutine, and then releases the build. Close must return within a generous deadline
// (it does not block on the wedged build — the build runs without c.mu held). The late-built client is
// discarded and closed rather than installed, no client remains, and a subsequent GetClient() returns
// nil without panicking.
//
// The build is bounded by the SDK init timeout in production; here the gate release unblocks it
// deterministically. This test is run under -race to catch any unsynchronized access between the
// Close teardown and the re-anchor's post-build lock re-acquisition.
func TestReanchorClosedDuringInFlightBuild(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	inner := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	// The new anchor's build blocks on gate; entered signals the build has reached the factory (the
	// re-anchor is mid-flight, pre-commit, and holds no c.mu while it waits here).
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	gatedFactory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == reanchorCloseNewAnchor {
			entered <- struct{}{}
			<-gate
		}
		return inner(sdkKey, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, gatedFactory, mockLog.Loggers, readyCh)
	defer env.Close() // idempotent; the test closes explicitly below, this guards early failures

	require.Equal(t, env, requireEnvReady(t, readyCh))
	originalClient := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == originalClient }, time.Second, 10*time.Millisecond)

	// Drive the re-anchor on a background goroutine; it blocks in the gated build.
	start := time.Unix(2000, 0)
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: reanchorCloseNewAnchor}).
		WithSDKKey(credential.SDKKeyParams{Value: envConfig.SDKKey, Expiry: util.PtrOrNil(start.Add(time.Hour))}).
		Build()
	require.NoError(t, err)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		env.(*envContextImpl).reconcileCredentials(set, start)
	}()

	<-entered // the build is wedged: the re-anchor is mid-flight, pre-commit.

	// Close from another goroutine. It does not hold reconcileMu, and the re-anchor released c.mu around
	// the build, so Close can proceed to tear down the env while the build is still blocked.
	closeDone := make(chan error, 1)
	go func() { closeDone <- env.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return while the re-anchor build was wedged")
	}

	// Release the wedged build. The late client is built and returned to the re-anchor, which finds the
	// env closed and discards + closes it rather than installing it.
	close(gate)
	<-reconcileDone

	// The late-built client was closed (its close channel fires) and never installed.
	lateClient := requireClientReady(t, clientCh)
	lateClient.AwaitClose(t, time.Second)

	envImpl := env.(*envContextImpl)
	envImpl.mu.RLock()
	remaining := len(envImpl.clients)
	envImpl.mu.RUnlock()
	assert.Equal(t, 0, remaining, "no client remains installed after Close during an in-flight re-anchor")

	// GetClient after Close returns nil without panicking.
	assert.Nil(t, env.GetClient(), "GetClient returns nil after Close, with no panic")
}
