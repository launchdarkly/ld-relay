package relayenv

// The key an environment is serving on stays usable for the whole re-anchor.
//
// A re-anchor moves the anchor pointer only after the new key's client is built, which takes up to
// Main.InitTimeout. Throughout that build the environment still answers requests on the previous
// anchor. A payload that revokes that key outright must therefore not drop it from the accepted set
// until the anchor actually moves, or GetStreamHandler answers 404 for SDKs authenticating with the
// very key relay is serving from.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const servingKeyTestNewAnchor = config.SDKKey("serving-key-new-anchor")

// alwaysOKStreamProvider yields a 200 handler for any credential, so a 404 from GetStreamHandler can
// only come from its accepted-set check.
type alwaysOKStreamProvider struct{}

func (alwaysOKStreamProvider) Handler(_ sdkauth.ScopedCredential) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
}

func (alwaysOKStreamProvider) Register(
	_ sdkauth.ScopedCredential, _ streams.EnvStoreQueries, _ ldlog.Loggers,
) streams.EnvStreamProvider {
	return nil
}

func (alwaysOKStreamProvider) Close() {}

// streamStatusFor drives a real request through GetStreamHandler and returns the status code.
func streamStatusFor(env *envContextImpl, cred credential.SDKCredential) int {
	handler := env.GetStreamHandler(alwaysOKStreamProvider{}, cred)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/all", nil))
	return rec.Code
}

// revokeAnchorSet designates newAnchor as the only SDK key, revoking every other key outright.
func revokeAnchorSet(t *testing.T, newAnchor config.SDKKey) credential.AcceptedSet {
	t.Helper()
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: newAnchor}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: st.EnvMain.Config.MobileKey}).
		WithEnvironmentID(st.EnvMain.Config.EnvID).
		Build()
	require.NoError(t, err)
	return set
}

// servingKeyProbe observes the serving key's stream status from inside the new anchor's client build,
// which is the window where c.mu is released and the accepted set has already been re-diffed.
type servingKeyProbe struct {
	mu       sync.Mutex
	statuses []int
	impl     *envContextImpl
}

func (p *servingKeyProbe) record(servingKey config.SDKKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.statuses = append(p.statuses, streamStatusFor(p.impl, servingKey))
}

func (p *servingKeyProbe) snapshot() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.statuses...)
}

// newServingKeyEnv builds an environment whose new-anchor build calls probe.record before returning
// buildResult, so the test can observe state mid-build.
func newServingKeyEnv(
	t *testing.T,
	probe *servingKeyProbe,
	buildFails bool,
) *envContextImpl {
	t.Helper()
	mockLog := ldlogtest.NewMockLog()
	t.Cleanup(func() { mockLog.DumpIfTestFailed(t) })

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthy := testclient.FakeLDClientFactoryWithChannel(true, clientCh)
	factory := func(key config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if key != servingKeyTestNewAnchor {
			return healthy(key, cfg, timeout)
		}
		probe.record(st.EnvMain.Config.SDKKey)
		if buildFails {
			return nil, errors.New("serving-key test: build refused")
		}
		return healthy(key, cfg, timeout)
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, st.EnvMain.Config, factory, mockLog.Loggers, readyCh)
	t.Cleanup(func() { _ = env.Close() })
	require.Equal(t, env, requireEnvReady(t, readyCh))
	requireClientReady(t, clientCh)

	envImpl := env.(*envContextImpl)
	probe.impl = envImpl
	return envImpl
}

// TestServingKeyStaysAuthenticatedDuringSuccessfulReanchor is the regression test for the defect: a
// rotation that revokes the outgoing anchor must not 404 that key while its replacement is being built.
// This holds for an ordinary, entirely successful rotation -- no rollback and no retry involved.
func TestServingKeyStaysAuthenticatedDuringSuccessfulReanchor(t *testing.T) {
	servingKey := st.EnvMain.Config.SDKKey
	probe := &servingKeyProbe{}
	envImpl := newServingKeyEnv(t, probe, false)

	require.Equal(t, http.StatusOK, streamStatusFor(envImpl, servingKey), "sanity: usable before the rotation")

	now := time.Unix(2000, 0)
	envImpl.reconcileCredentials(revokeAnchorSet(t, servingKeyTestNewAnchor), now)

	for i, code := range probe.snapshot() {
		assert.Equal(t, http.StatusOK, code,
			"build attempt %d: the key the environment is serving on must not be rejected", i+1)
	}
	require.Len(t, probe.snapshot(), 1, "the rotation built the new anchor's client exactly once")

	// After the commit the rotation is complete, so the revoked key is correctly rejected.
	assert.Equal(t, servingKeyTestNewAnchor, envImpl.keyRotator.AnchorKey())
	assert.Equal(t, http.StatusNotFound, streamStatusFor(envImpl, servingKey),
		"once the anchor has moved, the revoked key is no longer accepted")
	assert.NotContains(t, envImpl.GetCredentials(), credential.SDKCredential(servingKey))
}

// TestServingKeyStaysAuthenticatedAcrossReanchorRetries covers the same window on the retry path, where
// it recurs once per credential cleanup interval for as long as the build keeps failing.
func TestServingKeyStaysAuthenticatedAcrossReanchorRetries(t *testing.T) {
	servingKey := st.EnvMain.Config.SDKKey
	probe := &servingKeyProbe{}
	envImpl := newServingKeyEnv(t, probe, true)

	now := time.Unix(2000, 0)
	envImpl.reconcileCredentials(revokeAnchorSet(t, servingKeyTestNewAnchor), now)
	envImpl.triggerCredentialChanges(now.Add(time.Minute))
	envImpl.triggerCredentialChanges(now.Add(2 * time.Minute))

	statuses := probe.snapshot()
	require.GreaterOrEqual(t, len(statuses), 3, "the retry rebuilt the client on each cleanup interval")
	for i, code := range statuses {
		assert.Equal(t, http.StatusOK, code,
			"build attempt %d: the serving key must stay usable on every retry", i+1)
	}

	// The rollback keeps the environment on the serving key, so it stays usable between retries too.
	assert.Equal(t, servingKey, envImpl.keyRotator.AnchorKey())
	assert.Equal(t, http.StatusOK, streamStatusFor(envImpl, servingKey))
	assert.Contains(t, envImpl.GetCredentials(), credential.SDKCredential(servingKey))
}
