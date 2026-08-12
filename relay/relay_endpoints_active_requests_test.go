package relay

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
	"unicode/utf8"

	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/eventsource"

	"go.opentelemetry.io/otel/attribute"
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
		{
			// A path that matches a route but with the wrong method. Relay never answers 405, so this
			// does not depend on Router.MethodNotAllowedHandler: the catch-all serverSideRouter
			// (PathPrefix "") matches the path, which clears the pending ErrMethodMismatch
			// (gorilla/mux route.go:69-79), and the request ends up on the not-found handler. So it is
			// counted, and MethodNotAllowedHandler stays nil rather than changing the response code.
			name: "wrong method on a real route",
			buildReq: func() *http.Request {
				return st.BuildRequest("POST", "/status", nil, nil)
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

// TestWrongMethodIsNotFoundAndIsCounted pins down why Router.MethodNotAllowedHandler does not need the
// same treatment as Router.NotFoundHandler. gorilla/mux does assign MethodNotAllowedHandler without
// building the middleware chain, but that branch is unreachable in this router: the catch-all
// serverSideRouter (PathPrefix "") matches every path, and a matcher that succeeds clears a pending
// ErrMethodMismatch, so Match finishes with ErrNotFound instead. Relay therefore never answers 405,
// and a wrong-method request is counted under not_provided.
//
// If a future change removes the catch-all subrouter, this test fails with a 405, which is the signal
// to wrap MethodNotAllowedHandler as well.
func TestWrongMethodIsNotFoundAndIsCounted(t *testing.T) {
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		reader := installActiveRequestReader(t, p.relay)

		for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			result, _ := st.DoRequest(st.BuildRequest(method, "/status", nil, nil), p.relay)
			assert.Equal(t, http.StatusNotFound, result.StatusCode,
				"%s /status should be 404, not 405 -- see the comment on this test", method)
		}

		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		m := st.FindMetricByName(&rm, activeRequestsMetricName)
		require.NotNil(t, m, "wrong-method requests were not counted at all")

		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok)
		methods := map[string]bool{}
		for _, dp := range sum.DataPoints {
			endpointType, ok := dp.Attributes.Value("relay.endpoint.type")
			require.True(t, ok)
			assert.Equal(t, string(metrics.EndpointTypeNotProvided), endpointType.AsString())
			if method, ok := dp.Attributes.Value("http.request.method"); ok {
				methods[method.AsString()] = true
			}
		}
		for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			assert.True(t, methods[method], "%s was not recorded in active requests", method)
		}
	})
}

// TestRecordedAttributesAreValidUTF8 covers the whole chain from a hostile header to the recorded
// attribute set. Attribute values are serialized into OTLP protobuf string fields, which proto3
// requires to be valid UTF-8; one bad byte fails the marshal for the entire export batch, and because
// these series are cumulative the failure repeats every interval until the process restarts.
//
// Header values are not restricted to ASCII -- RFC 7230 permits obs-text and Go's HTTP parser passes
// those bytes through unchanged -- and the status and not-found handlers record metrics without
// requiring credentials, so an unauthenticated caller can reach this.
func TestRecordedAttributesAreValidUTF8(t *testing.T) {
	const invalid = "\xff\xfe"

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		reader := installActiveRequestReader(t, p.relay)

		poison := func(req *http.Request) *http.Request {
			req.Header.Set("User-Agent", "agent-"+invalid)
			req.Header.Set("X-LaunchDarkly-Wrapper", "wrapper-"+invalid)
			req.Header.Set("X-LaunchDarkly-Instance-Id", "instance-"+invalid)
			req.Header.Set("X-LaunchDarkly-Tags",
				"application-id/app-"+invalid+" application-version/ver-"+invalid)
			return req
		}

		// Unauthenticated first: these paths need no credentials at all.
		st.DoRequest(poison(st.BuildRequest("GET", "/no/such/route", nil, nil)), p.relay)
		st.DoRequest(poison(st.BuildRequest("GET", "/status", nil, nil)), p.relay)
		// Then an authenticated request, which carries the full attribute set.
		st.DoRequest(poison(st.BuildRequestWithAuth("GET", "/sdk/poll", st.EnvMain.Config.SDKKey, nil)), p.relay)

		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))

		checked := 0
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					continue
				}
				for _, dp := range sum.DataPoints {
					for _, kv := range dp.Attributes.ToSlice() {
						if kv.Value.Type() != attribute.STRING {
							continue
						}
						checked++
						assert.True(t, utf8.ValidString(kv.Value.AsString()),
							"%s attribute %s is not valid UTF-8: %q -- this fails the whole OTLP export batch",
							m.Name, kv.Key, kv.Value.AsString())
					}
				}
			}
		}
		assert.Positive(t, checked, "no string attributes were examined")
	})
}
