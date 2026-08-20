package relayenv

// Stream handlers are no longer built or stored per credential. Instead
// GetStreamHandler resolves the request's credential to a scoped channel and asks the StreamProvider to
// build the handler on demand, scoping it with the env's (immutable) filter key. These tests exercise
// that on-demand path directly: that the provider is asked for the right scoped credential, that a valid
// credential yields the provider's handler, and that a credential the provider rejects (wrong kind) falls
// back to the 404 handler. Rejection of a credential that is no longer accepted is covered in
// env_context_stream_handler_revocation_test.go. End-to-end multi-key streaming through the full HTTP
// stack is covered by the relay-package concurrent-keys auth suite.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rotatorAccepting returns a rotator initialized with these credentials, any of which may be undefined.
// GetStreamHandler consults the accepted set before asking the provider for a handler, so a test
// envContextImpl needs a real rotator rather than the zero value.
func rotatorAccepting(
	sdkKey config.SDKKey,
	mobileKey config.MobileKey,
	envID config.EnvironmentID,
) *credential.Rotator {
	r := credential.NewRotator(ldlog.NewDisabledLoggers())
	r.Initialize(sdkKey, mobileKey, envID)
	return r
}

// rotatorAcceptingSDKKeys returns a rotator anchored on anchor with others accepted alongside it.
// Initialize takes a single SDK key, so a multi-key accepted set is reached the way a relay reaches one:
// by reconciling.
func rotatorAcceptingSDKKeys(t *testing.T, anchor config.SDKKey, others ...config.SDKKey) *credential.Rotator {
	t.Helper()

	builder := credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: anchor})
	for _, key := range others {
		builder.WithSDKKey(credential.SDKKeyParams{Value: key})
	}

	r := credential.NewRotator(ldlog.NewDisabledLoggers())
	now := time.Unix(1000, 0)
	result := r.Reconcile(mustBuildAcceptedSet(t, builder), now)
	require.NotNil(t, result.AnchorChange, "the first anchor is signaled as a change")
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	r.StepTime(now)

	return r
}

// fakeStreamProvider records the scoped credential passed to Handler and returns a caller-supplied
// handler for credentials it accepts (nil otherwise, mimicking a real provider rejecting the wrong
// credential kind). Register/Close are unused by GetStreamHandler.
type fakeStreamProvider struct {
	handlerFn func(sdkauth.ScopedCredential) http.HandlerFunc
	scopes    []sdkauth.ScopedCredential
}

func (f *fakeStreamProvider) Handler(scoped sdkauth.ScopedCredential) http.HandlerFunc {
	f.scopes = append(f.scopes, scoped)
	return f.handlerFn(scoped)
}

func (f *fakeStreamProvider) Register(sdkauth.ScopedCredential, streams.EnvStoreQueries, ldlog.Loggers) streams.EnvStreamProvider {
	return nil
}

func (f *fakeStreamProvider) Close() {}

func TestGetStreamHandler_BuildsOnDemandScopedWithEnvFilterKey(t *testing.T) {
	const filter = config.FilterKey("my-filter")

	served := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	sp := &fakeStreamProvider{
		handlerFn: func(scoped sdkauth.ScopedCredential) http.HandlerFunc {
			// Accept SDK keys only, as a server-side provider would; reject other credential kinds.
			if _, ok := scoped.SDKCredential.(config.SDKKey); ok {
				return served
			}
			return nil
		},
	}

	c := &envContextImpl{
		filterKey:  filter,
		keyRotator: rotatorAcceptingSDKKeys(t, config.SDKKey("sdk-A"), config.SDKKey("sdk-B")),
	}

	// A valid (right-kind) credential: the provider is asked for that credential scoped with the env's
	// filter key, and its handler is returned as-is (no per-credential storage, built on the spot).
	h := c.GetStreamHandler(sp, config.SDKKey("sdk-A"))
	require.Len(t, sp.scopes, 1)
	assert.Equal(t, filter, sp.scopes[0].FilterKey, "scoped with the env's filter key")
	assert.Equal(t, config.SDKKey("sdk-A"), sp.scopes[0].SDKCredential, "scoped with the request credential")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, 299, rr.Code, "the provider's on-demand handler is returned for a valid credential")

	// A second, different credential resolves independently through the same code path -- there is no
	// shared/cached per-credential handler, each call re-derives the scoped channel.
	c.GetStreamHandler(sp, config.SDKKey("sdk-B"))
	require.Len(t, sp.scopes, 2)
	assert.Equal(t, config.SDKKey("sdk-B"), sp.scopes[1].SDKCredential)
}

func TestGetStreamHandler_WrongKindCredentialServes404(t *testing.T) {
	sp := &fakeStreamProvider{
		handlerFn: func(scoped sdkauth.ScopedCredential) http.HandlerFunc {
			if _, ok := scoped.SDKCredential.(config.SDKKey); ok {
				return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
			}
			return nil // provider does not support this credential kind
		},
	}

	// The mobile key is deliberately in the accepted set: this test is about the provider rejecting the
	// wrong credential kind, so the accepted-set check must pass in order for the request to reach it.
	c := &envContextImpl{
		filterKey:  config.DefaultFilter,
		keyRotator: rotatorAccepting("", config.MobileKey("mob-key"), ""),
	}

	// A credential the provider rejects (returns nil for) must fall back to the invalid-stream 404
	// handler, exactly as the old per-credential map miss did.
	h := c.GetStreamHandler(sp, config.MobileKey("mob-key"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
