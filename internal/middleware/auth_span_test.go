package middleware

import (
	"net/http"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRequestSpanName stands in for the otelmux request span that wraps every real relay request.
const testRequestSpanName = "test.request"

// withRecordedSpans installs a real (non-noop) tracer provider with an in-memory recorder for the
// duration of f, and returns the spans that were ended.
func withRecordedSpans(t *testing.T, f func()) tracetest.SpanStubs {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	f()

	require.NoError(t, tp.ForceFlush(t.Context()))
	return tracetest.SpanStubsFromReadOnlySpans(recorder.Ended())
}

// runWithRequestSpan sends req through selector inside a parent span, standing in for the otelmux
// request span, and returns every recorded span.
func runWithRequestSpan(t *testing.T, selector func(http.Handler) http.Handler, req *http.Request) tracetest.SpanStubs {
	t.Helper()
	return withRecordedSpans(t, func() {
		ctx, requestSpan := tracing.Tracer().Start(req.Context(), testRequestSpanName)
		defer requestSpan.End()

		resp, _ := st.DoRequest(req.WithContext(ctx), selector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	require.FailNow(t, "no span named "+name+" was recorded")
	return tracetest.SpanStub{}
}

func spanAttr(s tracetest.SpanStub, key attribute.Key) (attribute.Value, bool) {
	for _, kv := range s.Attributes {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// The environment attributes go on the request span, not the auth span, so they cover every
// downstream handler span in the trace.
func TestEnvAttributesAreSetOnTheRequestSpan(t *testing.T) {
	env := testenv.NewTestEnvContextWithEnvConfig("ProjectName JSClientSideEnv", st.EnvClientSide.Config, true, nil)
	envs := testEnvironments{envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
		sdkauth.New(st.EnvClientSide.Config.SDKKey): env,
	}}
	selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

	spans := runWithRequestSpan(t, selector, buildPreRoutedRequestWithAuth(st.EnvClientSide.Config.SDKKey))

	requestSpan := findSpan(t, spans, testRequestSpanName)
	name, ok := spanAttr(requestSpan, tracing.EnvNameKey)
	require.True(t, ok, "environment.name missing from the request span")
	assert.Equal(t, "ProjectName JSClientSideEnv", name.AsString())

	id, ok := spanAttr(requestSpan, tracing.EnvIDKey)
	require.True(t, ok, "environment.id missing from the request span")
	assert.Equal(t, string(st.EnvClientSide.Config.EnvID), id.AsString())

	// The auth span still reports the auth outcome, and does not duplicate the environment.
	authSpan := findSpan(t, spans, tracing.SpanAuth)
	result, ok := spanAttr(authSpan, tracing.AuthResultKey)
	require.True(t, ok)
	assert.Equal(t, "success", result.AsString())
	_, ok = spanAttr(authSpan, tracing.EnvNameKey)
	assert.False(t, ok, "the environment belongs on the request span, not the auth span")
}

// A filtered environment's display name contains a slash, which the sanitizer replaces so the span
// and the environment.name metric attribute report the same form of the name.
func TestEnvSpanNameIsSanitized(t *testing.T) {
	env := testenv.NewTestEnvContext("ProjectName Production/mobile", true, nil)
	envs := testEnvironments{envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
		sdkauth.New(st.EnvMain.Config.SDKKey): env,
	}}
	selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

	spans := runWithRequestSpan(t, selector, buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey))

	requestSpan := findSpan(t, spans, testRequestSpanName)
	name, ok := spanAttr(requestSpan, tracing.EnvNameKey)
	require.True(t, ok, "environment.name missing from the request span")
	assert.Equal(t, "ProjectName Production_mobile", name.AsString())

	// This environment has no client-side environment ID, so the ID attribute is omitted entirely.
	_, ok = spanAttr(requestSpan, tracing.EnvIDKey)
	assert.False(t, ok, "environment.id should be omitted when no EnvironmentID is configured")
}

// The client-side auth middleware sets the same attributes on the same span.
func TestEnvAttributesAreSetOnTheRequestSpanForClientSideAuth(t *testing.T) {
	env := testenv.NewTestEnvContextWithEnvConfig("ProjectName JSClientSideEnv", st.EnvClientSide.Config, true, nil)
	envs := testEnvironments{envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
		sdkauth.New(st.EnvClientSide.Config.EnvID): env,
	}}
	selector := SelectEnvironmentByClientSideAuth(envs)

	// An environment ID carries no authorization header value of its own, so set it directly the
	// way a browser SDK does.
	headers := make(http.Header)
	headers.Set("Authorization", string(st.EnvClientSide.Config.EnvID))
	spans := runWithRequestSpan(t, selector, buildPreRoutedRequest("GET", nil, headers, nil, nil))

	requestSpan := findSpan(t, spans, testRequestSpanName)
	name, ok := spanAttr(requestSpan, tracing.EnvNameKey)
	require.True(t, ok, "environment.name missing from the request span")
	assert.Equal(t, "ProjectName JSClientSideEnv", name.AsString())

	id, ok := spanAttr(requestSpan, tracing.EnvIDKey)
	require.True(t, ok, "environment.id missing from the request span")
	assert.Equal(t, string(st.EnvClientSide.Config.EnvID), id.AsString())
}
