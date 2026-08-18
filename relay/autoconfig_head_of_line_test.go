package relay

// End-to-end regression coverage for head-of-line blocking in auto-configuration processing, driven
// through the real RAC handler. Intra-environment ordering — the guarantee that must survive making
// environments independent — is covered at the unit level by
// TestSerializedActions_SameEnvironmentStaysOrdered.

import (
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/require"
)

// TestAutoConfigSlowReanchorDoesNotDelayOtherEnvironments is the regression test for the
// head-of-line blocking defect: an environment whose anchor rotation is stuck building its upstream
// SDK client must not delay an unrelated environment's rotation.
//
// The mechanism being guarded: StreamManager consumes the auto-configuration stream on one
// goroutine, and the re-anchor triggered by a rotation builds its client synchronously, blocking for
// up to Main.InitTimeout. Running those actions inline on the stream goroutine meant every other
// environment's updates — including credential revocations — waited behind it, so a bulk rotation
// across N environments cost N * InitTimeout in the worst case.
//
// Note both environments here rotate via the singular sdkKey field with no expiring slot, i.e. an
// ordinary single-key rotation. That shape is synthesized into the accepted-key model and so still
// moves the anchor, which is why this blocking affects any auto-configured environment and not only
// ones using concurrent keys.
func TestAutoConfigSlowReanchorDoesNotDelayOtherEnvironments(t *testing.T) {
	env1Rotated := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
	env2Rotated := makeEnvWithModifiedSDKKey(testAutoConfEnv2)

	// Env 1's re-anchor build parks in the factory until released, standing in for a slow or
	// unreachable upstream. buildStarted reports that it has actually begun blocking, so the test
	// never races ahead of the stall it depends on.
	//
	// The release is deferred inside the test body below as well as called explicitly, and must run
	// before the harness closes the relay: a stalled build holds the environment's reconcile lock,
	// which the credential-cleanup ticker also takes, and EnvContext.Close waits for that ticker
	// goroutine to exit. Leaving the gate shut on an assertion failure would hang the shutdown instead
	// of reporting the failure.
	gate := make(chan struct{})
	releaseGate := sync.OnceFunc(func() { close(gate) })
	buildStarted := make(chan struct{}, 1)

	makeFactory := func(createdCh chan<- *testclient.FakeLDClient) sdks.ClientFactoryFunc {
		healthy := testclient.FakeLDClientFactoryWithChannel(true, createdCh)
		return func(key config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
			if key == env1Rotated.SDKKey() {
				select {
				case buildStarted <- struct{}{}:
				default:
				}
				<-gate
			}
			return healthy(key, cfg, timeout)
		}
	}

	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1, testAutoConfEnv2)
	autoConfTestWithClientFactory(t, testAutoConfDefaultConfig, &initialEvent, makeFactory,
		func(p autoConfTestParams) {
			defer releaseGate()

			p.awaitClient()
			p.awaitClient()
			env1 := p.awaitEnvironment(testAutoConfEnv1.id)
			env2 := p.awaitEnvironment(testAutoConfEnv2.id)

			// Rotate env 1. Its re-anchor build blocks inside the factory and stays blocked.
			p.stream.Enqueue(makeAutoConfPatchEvent(env1Rotated))
			helpers.RequireValue(t, buildStarted, time.Second*5,
				"env 1's re-anchor build never started")

			// Rotate env 2 while env 1 is still stuck. This is the assertion that matters: before
			// per-environment serialization this patch could not even begin to be processed until env
			// 1's build returned or timed out.
			p.stream.Enqueue(makeAutoConfPatchEvent(env2Rotated))
			p.awaitCredentialsUpdated(env2, env2Rotated.params())

			// Env 2 completed its rotation and env 1 did not: exactly one new client was built, and it
			// is env 2's. Env 1 is isolated, not skipped or reordered.
			rotatedClient := p.awaitClient()
			require.Equal(t, env2Rotated.SDKKey(), rotatedClient.Key,
				"the only completed rotation should be env 2's")
			p.shouldNotCreateClient(100 * time.Millisecond)

			// Releasing the build lets env 1 finish its own rotation.
			releaseGate()
			env1Client := p.awaitClient()
			require.Equal(t, env1Rotated.SDKKey(), env1Client.Key)
			p.awaitCredentialsUpdated(env1, env1Rotated.params())
		})
}
