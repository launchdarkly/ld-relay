package relay

import (
	"context"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The semantic-convention attributes the HTTP tracing middleware derives from the request host.
// These are asserted by their wire names, because that is what a backend queries on.
const (
	serverAddressKey = attribute.Key("server.address")
	serverPortKey    = attribute.Key("server.port")
)

// TestRequestSpanReportsRequestHostAsServerAddress guards the argument passed to
// otelmux.Middleware. That argument is the "primary server name", and a non-empty value
// replaces the request host in server.address on every span, so passing a service name there
// makes every trace claim the client connected to that name. Relay passes an empty string so
// that both server.address and server.port come from the same request.
func TestRequestSpanReportsRequestHostAsServerAddress(t *testing.T) {
	recorder := installSpanRecorder(t)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		headers := make(http.Header)
		headers.Set("Authorization", string(st.EnvMain.Config.SDKKey))
		req := st.BuildRequest("GET", "http://relay.example.com:8031/sdk/flags", nil, headers)

		result, _ := st.DoRequest(req, p.relay)
		require.Equal(t, http.StatusOK, result.StatusCode)

		attrs := spanAttrs(rootSpan(t, recorder.Ended()))

		address, ok := attrs[serverAddressKey]
		require.True(t, ok, "request span is missing server.address")
		assert.Equal(t, "relay.example.com", address.AsString())

		port, ok := attrs[serverPortKey]
		require.True(t, ok, "request span is missing server.port")
		assert.Equal(t, int64(8031), port.AsInt64())
	})
}

// TestOtelmuxRecordsNoMetrics guards the meter provider passed to otelmux.Middleware. otelmux builds
// its own HTTP server instruments, keyed on the client-controlled Host, and without a provider of its
// own it takes the global one. The global provider is a no-op that delegates retroactively, so
// otelmux's instruments would come to life -- reporting a second http.server.request.duration under
// its own scope, and a series per Host -- the moment any code called otel.SetMeterProvider. Relay
// records its own request metrics with a trimmed attribute set instead.
func TestOtelmuxRecordsNoMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		// Restore the provider that was installed before, rather than leaving a hard no-op behind.
		// otel.SetMeterProvider only rewires already-created instruments once per process, so a no-op
		// left here can never be lit up again, and a later test asserting on global-provider metrics
		// would silently observe nothing.
		otel.SetMeterProvider(previous)
	})

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, host := range []string{"one.relay.example.com", "two.relay.example.com"} {
			req := st.BuildRequest("GET", "http://"+host+"/status", nil, nil)
			result, _ := st.DoRequest(req, p.relay)
			require.Equal(t, http.StatusOK, result.StatusCode)
		}

		var collected metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &collected))

		for _, scope := range collected.ScopeMetrics {
			// Only otelmux's own instruments are in question. Anything else reaching this provider is
			// a different subject, and failing on it here would report the wrong component.
			if scope.Scope.Name != otelmux.ScopeName {
				continue
			}
			for _, m := range scope.Metrics {
				assert.Fail(t, "otelmux recorded a metric through the global meter provider",
					"scope %q reported %q", scope.Scope.Name, m.Name)
			}
		}

		// A canary proves the reader is wired up and would have seen a metric if one had been
		// recorded, so the assertion above cannot pass merely because nothing was collected.
		counter, err := provider.Meter("canary").Int64Counter("canary.requests")
		require.NoError(t, err)
		counter.Add(context.Background(), 1)
		require.NoError(t, reader.Collect(context.Background(), &collected))
		require.NotEmpty(t, collected.ScopeMetrics, "the manual reader collected nothing at all")
	})
}
