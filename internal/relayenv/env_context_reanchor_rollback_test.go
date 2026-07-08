package relayenv

// Regression: a grace-demotion re-anchor rollback must not leave the still-authoritative anchor exposed
// to the cleanup ticker.
//
// Scenario (the default backend rotation): anchor A is demoted with a +1h grace expiry while brand-new
// key B is designated the new anchor. B's client fails to initialize, so the synchronous re-anchor rolls
// back: A stays the anchor and keeps serving. A's accepted entry still carries the grace expiry and
// CommitAnchor never ran (A is still anchorKey), so the cleanup ticker firing past the grace window must
// NOT expire A and close the env's only client -- StepTime's anchor guard prevents that. Without the
// guard, GetClient() returns nil while the rotator still names A the anchor: a silent upstream outage.

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReanchorRollbackGraceDemotionKeepsAnchorServing(t *testing.T) {
	envConfig := st.EnvMain.Config
	fakeErr := errors.New("re-anchor: new client init refused")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthyFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)
	// Fail only for the new anchor; the original anchor builds fine.
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
	// Re-anchor A->B with A grace-demoted (+1h). B's build fails, so this rolls back.
	reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

	// Rollback holds immediately: A is still the anchor and still serving.
	require.Same(t, originalClient, env.GetClient(), "rollback keeps the old anchor serving")
	require.Equal(t, envConfig.SDKKey, env.(*envContextImpl).keyRotator.AnchorKey(), "anchor stayed on the old key")

	// The cleanup ticker fires after the grace window. A is STILL the anchor, so it must keep serving --
	// a key's grace expiry must not apply to it while it is the authoritative anchor.
	env.(*envContextImpl).triggerCredentialChanges(now.Add(time.Hour + time.Minute))

	assert.Equal(t, envConfig.SDKKey, env.(*envContextImpl).keyRotator.AnchorKey(),
		"the anchor pointer is unchanged after the ticker")
	assert.NotNil(t, env.GetClient(),
		"the cleanup ticker must not reap the still-authoritative anchor's client (env would go dark)")
}
