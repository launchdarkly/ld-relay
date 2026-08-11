package relay

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/eventsource"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const activeRequestsMetricName = "http.server.active_requests"

// installActiveRequestReader swaps in reader-backed instruments for a started relay. The middleware
// resolves instruments through the Manager per request, so this takes effect for requests made after
// the swap.
func installActiveRequestReader(t *testing.T, relay *Relay) sdkmetric.Reader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	instruments, err := metrics.NewInstrumentsForTest(meterProvider.Meter("ld-relay"))
	require.NoError(t, err)
	relay.metricsManager.SetInstrumentsForTest(instruments)
	return reader
}

// requireSingleActiveRequestPoint collects the active requests metric and requires that it holds
// exactly one series, returning that data point. One series per request is what proves the increment
// and the decrement used an identical attribute set.
func requireSingleActiveRequestPoint(t *testing.T, reader sdkmetric.Reader) metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	m := st.FindMetricByName(&rm, activeRequestsMetricName)
	require.NotNil(t, m, "%s was not recorded", activeRequestsMetricName)
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", activeRequestsMetricName)
	require.Len(t, sum.DataPoints, 1, "expected exactly one active requests series")
	return sum.DataPoints[0]
}

func requireEndpointType(t *testing.T, dp metricdata.DataPoint[int64], expected metrics.EndpointType) {
	t.Helper()
	val, ok := dp.Attributes.Value("relay.endpoint.type")
	require.True(t, ok, "relay.endpoint.type attribute not present")
	assert.Equal(t, string(expected), val.AsString())
}

// TestActiveRequestsCoverAllEndpointTypes drives a representative route for each endpoint type through
// the full relay and asserts that http.server.active_requests recorded it under the expected
// relay.endpoint.type. Polling, event ingestion, goals and status are all included: per the OTEL
// semantic convention this instrument counts every in-flight HTTP request, not just streams.
func TestActiveRequestsCoverAllEndpointTypes(t *testing.T) {
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	cases := []struct {
		name         string
		buildReq     func() *http.Request
		endpointType metrics.EndpointType
	}{
		{
			name: "server-side poll",
			buildReq: func() *http.Request {
				return st.BuildRequestWithAuth("GET", "/sdk/poll", st.EnvMain.Config.SDKKey, nil)
			},
			endpointType: metrics.EndpointTypePoll,
		},
		{
			name: "PHP poll",
			buildReq: func() *http.Request {
				return st.BuildRequestWithAuth("GET", "/sdk/flags", st.EnvMain.Config.SDKKey, nil)
			},
			endpointType: metrics.EndpointTypePoll,
		},
		{
			name: "mobile poll",
			buildReq: func() *http.Request {
				return st.BuildRequestWithAuth("GET", "/msdk/evalx/contexts/"+contextBase64,
					st.EnvWithAllCredentials.Config.MobileKey, nil)
			},
			endpointType: metrics.EndpointTypePoll,
		},
		{
			name: "client-side poll",
			buildReq: func() *http.Request {
				return st.BuildRequest("GET",
					"/sdk/evalx/"+string(st.EnvClientSide.Config.EnvID)+"/contexts/"+contextBase64, nil, nil)
			},
			endpointType: metrics.EndpointTypePoll,
		},
		{
			name: "server-side events",
			buildReq: func() *http.Request {
				return st.BuildRequestWithAuth("POST", "/bulk", st.EnvMain.Config.SDKKey, []byte(`[]`))
			},
			endpointType: metrics.EndpointTypeEvents,
		},
		{
			name: "mobile events",
			buildReq: func() *http.Request {
				return st.BuildRequestWithAuth("POST", "/mobile/events/bulk",
					st.EnvWithAllCredentials.Config.MobileKey, []byte(`[]`))
			},
			endpointType: metrics.EndpointTypeEvents,
		},
		{
			name: "client-side events",
			buildReq: func() *http.Request {
				return st.BuildRequest("POST",
					"/events/bulk/"+string(st.EnvClientSide.Config.EnvID), []byte(`[]`), nil)
			},
			endpointType: metrics.EndpointTypeEvents,
		},
		{
			name: "status",
			buildReq: func() *http.Request {
				return st.BuildRequest("GET", "/status", nil, nil)
			},
			endpointType: metrics.EndpointTypeStatus,
		},
		{
			name: "unmatched route",
			buildReq: func() *http.Request {
				return st.BuildRequest("GET", "/no/such/endpoint", nil, nil)
			},
			endpointType: metrics.EndpointTypeNotProvided,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var config c.Config
			config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvWithAllCredentials, st.EnvClientSide)

			withStartedRelay(t, config, func(p relayTestParams) {
				reader := installActiveRequestReader(t, p.relay)

				st.DoRequest(tc.buildReq(), p.relay)

				dp := requireSingleActiveRequestPoint(t, reader)
				requireEndpointType(t, dp, tc.endpointType)
				assert.Zero(t, dp.Value, "request finished, so it should no longer be counted as active")
			})
		})
	}
}

// TestActiveRequestsCountLiveStream asserts that a held-open SSE connection reads as active for as long
// as it is open, and returns to zero once it closes.
func TestActiveRequestsCountLiveStream(t *testing.T) {
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		reader := installActiveRequestReader(t, p.relay)

		req := st.BuildRequestWithAuth("GET", "/all", st.EnvMain.Config.SDKKey, nil)
		st.WithStreamRequest(t, req, p.relay, func(events <-chan eventsource.Event) {
			<-events // wait until the stream is established

			dp := requireSingleActiveRequestPoint(t, reader)
			requireEndpointType(t, dp, metrics.EndpointTypeStream)
			assert.Equal(t, int64(1), dp.Value, "an open stream should be counted as an active request")
		})

		dp := requireSingleActiveRequestPoint(t, reader)
		requireEndpointType(t, dp, metrics.EndpointTypeStream)
		assert.Zero(t, dp.Value, "a closed stream should no longer be counted as active")
	})
}
