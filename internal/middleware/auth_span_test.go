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

func findAuthSpan(t *testing.T, spans tracetest.SpanStubs) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == tracing.SpanAuth {
			return s
		}
	}
	require.FailNow(t, "no relay.auth span was recorded")
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

// The environment attributes are set on the auth span before next.ServeHTTP runs, and the span is
// not ended until that returns, so they have to survive an exporter round trip.
func TestAuthEnvAttributesReachTheExporter(t *testing.T) {
	env := testenv.NewTestEnvContextWithEnvConfig("ProjectName JSClientSideEnv", st.EnvClientSide.Config, true, nil)
	envs := testEnvironments{envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
		sdkauth.New(st.EnvClientSide.Config.SDKKey): env,
	}}
	selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

	spans := withRecordedSpans(t, func() {
		req := buildPreRoutedRequestWithAuth(st.EnvClientSide.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	span := findAuthSpan(t, spans)

	name, ok := spanAttr(span, tracing.AuthEnvNameKey)
	require.True(t, ok, "relay.auth.environment.name missing from exported span")
	assert.Equal(t, "ProjectName JSClientSideEnv", name.AsString())

	id, ok := spanAttr(span, tracing.AuthEnvIDKey)
	require.True(t, ok, "relay.auth.environment.id missing from exported span")
	assert.Equal(t, string(st.EnvClientSide.Config.EnvID), id.AsString())
}

// A filtered environment's display name contains a slash, which the sanitizer replaces so the span
// and the environment.name metric attribute report the same form of the name.
func TestAuthEnvSpanNameIsSanitizedOnTheExportedSpan(t *testing.T) {
	env := testenv.NewTestEnvContext("ProjectName Production/mobile", true, nil)
	envs := testEnvironments{envs: map[sdkauth.ScopedCredential]relayenv.EnvContext{
		sdkauth.New(st.EnvMain.Config.SDKKey): env,
	}}
	selector := SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, envs)

	spans := withRecordedSpans(t, func() {
		req := buildPreRoutedRequestWithAuth(st.EnvMain.Config.SDKKey)
		resp, _ := st.DoRequest(req, selector(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	span := findAuthSpan(t, spans)

	name, ok := spanAttr(span, tracing.AuthEnvNameKey)
	require.True(t, ok, "relay.auth.environment.name missing from exported span")
	assert.Equal(t, "ProjectName Production_mobile", name.AsString())

	// This environment has no client-side environment ID, so the ID attribute is omitted entirely.
	_, ok = spanAttr(span, tracing.AuthEnvIDKey)
	assert.False(t, ok, "relay.auth.environment.id should be omitted when no EnvironmentID is configured")
}
