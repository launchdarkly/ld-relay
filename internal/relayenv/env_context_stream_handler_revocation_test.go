package relayenv

// GetStreamHandler consults the rotator's accepted set before asking the StreamProvider for a handler.
// The middleware authenticates the request's credential once, up front, so a credential revoked while the
// request is still in flight would otherwise be handed a working stream. These tests drive revocation
// through the real Rotator.Reconcile path, and pair every expected 404 with a still-accepted control
// credential -- without the control, a handler that was simply broken for everything would pass.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamServedCode is the status the test provider's handler writes. It is deliberately not 200, so a
// served stream can never be confused with a status written by anything else in the chain.
const streamServedCode = 299

// alwaysServingProvider returns a handler for every credential kind, so any 404 in these tests can only
// have come from the accepted-set check -- never from a provider declining the credential's kind.
func alwaysServingProvider() *fakeStreamProvider {
	return &fakeStreamProvider{
		handlerFn: func(sdkauth.ScopedCredential) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(streamServedCode) }
		},
	}
}

// serveCode resolves cred through GetStreamHandler and reports the status its handler writes.
func serveCode(t *testing.T, c *envContextImpl, sp *fakeStreamProvider, cred credential.SDKCredential) int {
	t.Helper()
	rr := httptest.NewRecorder()
	c.GetStreamHandler(sp, cred).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	return rr.Code
}

func TestGetStreamHandlerRejectsRevokedCredentials(t *testing.T) {
	anchor := config.SDKKey("anchor-sdk-key")
	revokedSDK := config.SDKKey("revoked-sdk-key")
	primaryMobile := config.MobileKey("primary-mob-key")
	revokedMobile := config.MobileKey("revoked-mob-key")
	envID := config.EnvironmentID("env-id")
	now := time.Unix(1000, 0)

	r := credential.NewRotator(ldlog.NewDisabledLoggers())
	full, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: anchor}).
		WithSDKKey(credential.SDKKeyParams{Value: revokedSDK}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: primaryMobile}).
		WithMobileKey(credential.MobileKeyParams{Value: revokedMobile}).
		WithEnvironmentID(envID).
		Build()
	require.NoError(t, err)

	result := r.Reconcile(full, now)
	require.NotNil(t, result.AnchorChange, "the first anchor is signaled as a change")
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	r.StepTime(now)

	c := &envContextImpl{filterKey: config.DefaultFilter, keyRotator: r}
	sp := alwaysServingProvider()

	// Baseline: every accepted credential kind reaches the provider's handler.
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, anchor), "anchor")
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, revokedSDK), "SDK key, before revocation")
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, primaryMobile), "primary mobile key")
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, revokedMobile), "mobile key, before revocation")
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, envID), "environment ID")

	// Revoke the two non-primary keys outright by reconciling to a set that omits them.
	reduced, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: anchor}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: primaryMobile}).
		WithEnvironmentID(envID).
		Build()
	require.NoError(t, err)
	r.Reconcile(reduced, now)
	r.StepTime(now)

	assert.Equal(t, http.StatusNotFound, serveCode(t, c, sp, revokedSDK), "revoked SDK key")
	assert.Equal(t, http.StatusNotFound, serveCode(t, c, sp, revokedMobile), "revoked mobile key")

	// The retained credentials are untouched, so the 404s above are attributable to the revocation.
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, anchor), "anchor is still accepted")
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, primaryMobile), "primary mobile key is still accepted")
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, envID), "environment ID is still accepted")
}

func TestGetStreamHandlerDoesNotConsultProviderForRevokedCredential(t *testing.T) {
	// The check short-circuits before the provider is asked for a handler, so a revoked credential can
	// never reach the point of creating a channel subscription.
	c := &envContextImpl{
		filterKey:  config.DefaultFilter,
		keyRotator: rotatorAccepting(config.SDKKey("accepted-sdk-key"), "", ""),
	}
	sp := alwaysServingProvider()

	assert.Equal(t, http.StatusNotFound, serveCode(t, c, sp, config.SDKKey("revoked-sdk-key")))
	assert.Empty(t, sp.scopes, "the provider must not be asked to build a handler for a revoked credential")

	// The accepted credential still goes through, and only then is the provider consulted.
	assert.Equal(t, streamServedCode, serveCode(t, c, sp, config.SDKKey("accepted-sdk-key")))
	require.Len(t, sp.scopes, 1)
	assert.Equal(t, config.SDKKey("accepted-sdk-key"), sp.scopes[0].SDKCredential)
}

func TestGetStreamHandlerRejectsForeignEnvironmentID(t *testing.T) {
	// JS client streams authenticate with the environment ID, so it goes through the same check: only this
	// environment's own ID is accepted.
	c := &envContextImpl{
		filterKey:  config.DefaultFilter,
		keyRotator: rotatorAccepting("", "", config.EnvironmentID("this-env")),
	}
	sp := alwaysServingProvider()

	assert.Equal(t, streamServedCode, serveCode(t, c, sp, config.EnvironmentID("this-env")))
	assert.Equal(t, http.StatusNotFound, serveCode(t, c, sp, config.EnvironmentID("other-env")))
}
