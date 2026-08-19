package relayenv

// Regression tests for retrying a rolled-back re-anchor.
//
// A re-anchor whose new client fails to build rolls back and leaves the environment serving on the
// previous anchor -- the key LaunchDarkly is in the process of invalidating. Nothing else re-drives it:
// the payload's version was already recorded by the autoconfig MessageReceiver, so LaunchDarkly's
// re-put of the same payload is deduplicated to a noop and a stream reconnect does not help. The
// credential cleanup ticker replays the rolled-back set until it commits.

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reanchorRetryTestKey2 = config.SDKKey("reanchor-retry-new-anchor")

// hasPendingReanchor reports whether a rolled-back re-anchor is armed for the cleanup ticker to retry.
// It reads the field under reconcileMu, the lock that guards it. Production code has no such accessor:
// the retry is observable through the environment's behavior and its logs, not through its API.
func (c *envContextImpl) hasPendingReanchor() bool {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	return c.pendingReanchor != nil
}

// countingFactory wraps a client factory, counting build attempts per SDK key and failing the ones
// named in failFor until stopFailing is called.
type countingFactory struct {
	mu       sync.Mutex
	attempts map[config.SDKKey]int
	failing  map[config.SDKKey]bool
	delegate sdks.ClientFactoryFunc
}

func newCountingFactory(delegate sdks.ClientFactoryFunc, failFor ...config.SDKKey) *countingFactory {
	f := &countingFactory{
		attempts: make(map[config.SDKKey]int),
		failing:  make(map[config.SDKKey]bool),
		delegate: delegate,
	}
	for _, key := range failFor {
		f.failing[key] = true
	}
	return f
}

func (f *countingFactory) build(key config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
	f.mu.Lock()
	f.attempts[key]++
	failing := f.failing[key]
	f.mu.Unlock()
	if failing {
		return nil, errors.New("re-anchor: new client init refused")
	}
	return f.delegate(key, cfg, timeout)
}

func (f *countingFactory) attemptsFor(key config.SDKKey) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[key]
}

func (f *countingFactory) stopFailing(key config.SDKKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failing, key)
}

// newRetryTestEnv builds an environment whose client factory fails for failFor, and returns it along
// with the factory and the environment's first client.
func newRetryTestEnv(t *testing.T, failFor ...config.SDKKey) (*envContextImpl, *countingFactory, *testclient.FakeLDClient) {
	t.Helper()
	mockLog := ldlogtest.NewMockLog()
	t.Cleanup(func() { mockLog.DumpIfTestFailed(t) })

	clientCh := make(chan *testclient.FakeLDClient, 10)
	factory := newCountingFactory(testclient.FakeLDClientFactoryWithChannel(true, clientCh), failFor...)

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, st.EnvMain.Config, factory.build, mockLog.Loggers, readyCh)
	require.Equal(t, env, requireEnvReady(t, readyCh))

	client := requireClientReady(t, clientCh)
	require.Eventually(t, func() bool { return env.GetClient() == client }, time.Second, 10*time.Millisecond)

	return env.(*envContextImpl), factory, client
}

// rotationSet builds the payload the backend sends for a default rotation: newAnchor becomes the
// permanent anchor and oldAnchor is demoted with a one-hour grace expiry.
func rotationSet(t *testing.T, newAnchor, oldAnchor config.SDKKey, now time.Time) credential.AcceptedSet {
	t.Helper()
	graceExpiry := now.Add(time.Hour)
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: newAnchor}).
		WithSDKKey(credential.SDKKeyParams{Value: oldAnchor, Expiry: &graceExpiry}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: st.EnvMain.Config.MobileKey}).
		WithEnvironmentID(st.EnvMain.Config.EnvID).
		Build()
	require.NoError(t, err)
	return set
}

// anchorOnlySet designates anchor as the environment's only SDK key, revoking every other key outright.
func anchorOnlySet(t *testing.T, anchor config.SDKKey) credential.AcceptedSet {
	t.Helper()
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: anchor}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: st.EnvMain.Config.MobileKey}).
		WithEnvironmentID(st.EnvMain.Config.EnvID).
		Build()
	require.NoError(t, err)
	return set
}

// TestReanchorRetry_RolledBackReanchorIsRetriedByCleanupTicker is the regression basis for the defect:
// a transient SDK client build failure during a rotation must not permanently strand the environment
// on the outgoing anchor. The build is attempted once by the reconcile, fails, and rolls back; the
// cleanup ticker retries until it commits.
func TestReanchorRetry_RolledBackReanchorIsRetriedByCleanupTicker(t *testing.T) {
	envConfig := st.EnvMain.Config
	envImpl, factory, originalClient := newRetryTestEnv(t, reanchorRetryTestKey2)
	defer envImpl.Close()
	require.NoError(t, envImpl.GetStore().Init(st.AllData))

	now := time.Unix(2000, 0)
	set := rotationSet(t, reanchorRetryTestKey2, envConfig.SDKKey, now)

	// First attempt: the build fails and the re-anchor rolls back.
	envImpl.reconcileCredentials(set, now)
	require.Equal(t, 1, factory.attemptsFor(reanchorRetryTestKey2), "the reconcile attempts the build once")
	require.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "anchor stayed on the previous key")
	require.Same(t, originalClient, envImpl.GetClient(), "the previous anchor's client keeps serving")
	require.True(t, envImpl.hasPendingReanchor(), "the rolled-back set must be armed for retry")

	// The ticker retries while the failure persists. This is the assertion the defect fails: without a
	// retry path the build is attempted exactly once, ever.
	envImpl.triggerCredentialChanges(now.Add(time.Minute))
	require.GreaterOrEqual(t, factory.attemptsFor(reanchorRetryTestKey2), 2,
		"the cleanup ticker must retry the rolled-back re-anchor")
	require.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "still rolled back while the build fails")
	require.True(t, envImpl.hasPendingReanchor())

	// The transient failure clears; the next ticker commits the re-anchor.
	factory.stopFailing(reanchorRetryTestKey2)
	envImpl.triggerCredentialChanges(now.Add(2 * time.Minute))

	assert.Equal(t, reanchorRetryTestKey2, envImpl.keyRotator.AnchorKey(), "the retry committed the new anchor")
	assert.NoError(t, envImpl.GetInitError(), "a committed re-anchor clears any init error")
	assert.False(t, envImpl.hasPendingReanchor(), "a committed re-anchor disarms the retry")
	originalClient.AwaitClose(t, time.Second) // the demoted anchor's client closes once the commit lands

	// The demoted key keeps authenticating downstream connections until its grace expiry passes. The
	// expiry comes from the original payload, so the retries must not have extended it.
	assert.Contains(t, envImpl.GetCredentials(), credential.SDKCredential(envConfig.SDKKey))
	envImpl.triggerCredentialChanges(now.Add(2 * time.Hour))
	assert.NotContains(t, envImpl.GetCredentials(), credential.SDKCredential(envConfig.SDKKey),
		"the grace expiry from the original payload is honored, not extended by the retries")
}

// TestReanchorRetry_NewerPayloadEndsPendingRetry covers the two ways a newer payload ends a pending
// retry: it rotates to a different key that builds, or it cancels the rotation by naming the anchor the
// environment is already on. Either way the superseded set must not keep being replayed.
func TestReanchorRetry_NewerPayloadEndsPendingRetry(t *testing.T) {
	envConfig := st.EnvMain.Config
	thirdKey := config.SDKKey("reanchor-retry-third-anchor")

	for _, tc := range []struct {
		name           string
		newer          func(t *testing.T) credential.AcceptedSet
		expectedAnchor config.SDKKey
	}{
		{
			name:           "rotates to a different key",
			newer:          func(t *testing.T) credential.AcceptedSet { return anchorOnlySet(t, thirdKey) },
			expectedAnchor: thirdKey,
		},
		{
			name:           "cancels the rotation",
			newer:          func(t *testing.T) credential.AcceptedSet { return anchorOnlySet(t, envConfig.SDKKey) },
			expectedAnchor: envConfig.SDKKey,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envImpl, factory, _ := newRetryTestEnv(t, reanchorRetryTestKey2)
			defer envImpl.Close()

			now := time.Unix(2000, 0)
			envImpl.reconcileCredentials(rotationSet(t, reanchorRetryTestKey2, envConfig.SDKKey, now), now)
			require.True(t, envImpl.hasPendingReanchor())

			envImpl.reconcileCredentials(tc.newer(t), now)
			require.Equal(t, tc.expectedAnchor, envImpl.keyRotator.AnchorKey())
			assert.False(t, envImpl.hasPendingReanchor(), "the newer payload disarms the retry")

			attemptsBefore := factory.attemptsFor(reanchorRetryTestKey2)
			envImpl.triggerCredentialChanges(now.Add(time.Minute))
			assert.Equal(t, attemptsBefore, factory.attemptsFor(reanchorRetryTestKey2),
				"the ticker must not keep rebuilding the abandoned anchor")
			assert.Equal(t, tc.expectedAnchor, envImpl.keyRotator.AnchorKey())
		})
	}
}

// TestReanchorRetry_ClosedEnvDoesNotArmRetry covers a re-anchor that rolls back because the environment
// was torn down mid-build. The cleanup ticker has already stopped, so arming a retry would only log a
// promise that can never be kept.
func TestReanchorRetry_ClosedEnvDoesNotArmRetry(t *testing.T) {
	envConfig := st.EnvMain.Config
	envImpl, _, _ := newRetryTestEnv(t, reanchorRetryTestKey2)

	now := time.Unix(2000, 0)
	require.NoError(t, envImpl.Close())

	envImpl.reconcileCredentials(rotationSet(t, reanchorRetryTestKey2, envConfig.SDKKey, now), now)

	assert.False(t, envImpl.hasPendingReanchor(),
		"a closed environment must not arm a retry the stopped ticker can never run")
}
