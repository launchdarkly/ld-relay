package metrics

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"

	ldevents "github.com/launchdarkly/go-sdk-events/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// With OpenTelemetry disabled there are no instruments at all, rather than instruments backed by a
// noop meter. That is what allows the recording paths to skip building attribute sets for
// measurements that would be discarded, so it is worth asserting directly.
func TestNewManagerWithoutOpenTelemetryHasNoInstruments(t *testing.T) {
	manager, err := NewManager(config.OpenTelemetryConfig{}, 0, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	assert.Nil(t, manager.instruments)
	assert.Nil(t, manager.GetInstruments())
}

// Every recording path has to tolerate nil instruments, since that is the normal state when
// OpenTelemetry is disabled.
func TestRecordingIsSafeWithoutInstruments(t *testing.T) {
	manager, err := NewManager(config.OpenTelemetryConfig{}, 0, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	em, err := manager.AddEnvironment("testenv", nil)
	require.NoError(t, err)

	ri := RequestInfo{UserAgent: userAgentValue, Route: "/test", Method: "GET", EndpointType: EndpointTypePoll}

	assert.NotPanics(t, func() {
		StartActiveRequest(manager.GetInstruments(), em, ServerPlatformCategory, ri)()
		StartActiveRequest(manager.GetInstruments(), manager.GetUnscopedEnvironment(), "", ri)()
		RecordRequestDuration(context.Background(), manager.GetInstruments(), em, ri, time.Millisecond, ServerDuration)
		RecordEventsReceivedBytes(context.Background(), manager.GetInstruments(), em, ServerPlatformCategory, ri, 100)

		recorder := em.NewEventMetricsRecorder(manager.GetInstruments())
		recorder.RecordDroppedEvents(1)
		recorder.RecordEventsSent(1)
		recorder.RecordPendingEvents(1)
		recorder.RecordEventsBytesSent(1)
		recorder.RecordEventsFailedSend(1, ldevents.EventSendFailureMetadata{StatusCode: 500})
	})
}

func TestNewInstrumentsCreatesEveryInstrument(t *testing.T) {
	instruments, err := newInstruments(noop.Meter{})
	require.NoError(t, err)
	require.NotNil(t, instruments)

	assert.NotNil(t, instruments.connections)
	assert.NotNil(t, instruments.requestDuration)
	assert.NotNil(t, instruments.eventsReceivedBytes)
	assert.NotNil(t, instruments.eventsDropped)
	assert.NotNil(t, instruments.eventsSent)
	assert.NotNil(t, instruments.eventsFailedSend)
	assert.NotNil(t, instruments.eventsBytesSent)
	assert.NotNil(t, instruments.pendingEvents)
}

func TestAddEnvironmentWithoutEventPublisher(t *testing.T) {
	manager, err := NewManager(config.OpenTelemetryConfig{}, 0, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	env, err := manager.AddEnvironment("name", nil)

	assert.NoError(t, err)
	require.NotNil(t, env)
	assert.NotEqual(t, attribute.Set{}, env.GetAttributes())
}

func TestAddEnvironmentWithEventPublisher(t *testing.T) {
	publisher := newTestEventsPublisher()

	manager, err := NewManager(config.OpenTelemetryConfig{}, time.Millisecond*10, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	env, err := manager.AddEnvironment("name", publisher)

	assert.NoError(t, err)
	require.NotNil(t, env)
	assert.NotEqual(t, attribute.Set{}, env.GetAttributes())

	// Record something via the collector
	env.collector.RecordConnectionChange("server", userAgentValue, "", 1)
	env.FlushEventsExporter()

	metricsEvent := publisher.expectMetricsEvent(t, time.Second)
	assert.Equal(t, relayMetricsKind, metricsEvent.Kind)
	require.Len(t, metricsEvent.Connections, 1)
	assert.Equal(t, int64(1), metricsEvent.Connections[0].Current)
}

func TestAddEnvironmentAfterManagerClosed(t *testing.T) {
	manager, err := NewManager(config.OpenTelemetryConfig{}, 0, slog.Default())
	require.NoError(t, err)
	manager.Close()
	env, err := manager.AddEnvironment("name", nil)
	assert.Nil(t, env)
	assert.Error(t, err)
}

func TestRemoveEnvironment(t *testing.T) {
	manager, err := NewManager(config.OpenTelemetryConfig{}, 0, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	env, err := manager.AddEnvironment("name", nil)
	require.NoError(t, err)
	require.NotNil(t, env)

	manager.RemoveEnvironment(env)

	manager.lock.Lock()
	defer manager.lock.Unlock()
	assert.Len(t, manager.environments, 0)
}

func TestActiveRequestMetrics(t *testing.T) {
	specs := []struct {
		platform     string
		endpointType EndpointType
	}{
		{platform: BrowserPlatformCategory, endpointType: EndpointTypeStream},
		{platform: MobilePlatformCategory, endpointType: EndpointTypePoll},
		{platform: ServerPlatformCategory, endpointType: EndpointTypeEvents},
	}

	for _, tt := range specs {
		t.Run(tt.platform, func(t *testing.T) {
			testWithOTel(t, func(p testWithOTelParams) {
				ri := RequestInfo{UserAgent: userAgentValue, Route: "/test", Method: "GET", EndpointType: tt.endpointType}
				requestFinished := StartActiveRequest(p.instruments, p.env, tt.platform, ri)

				// While the request is in flight, the count should be 1
				rm, err := p.collectMetrics()
				require.NoError(t, err)
				m := findMetric(rm, connMeasureName)
				require.NotNil(t, m, "active requests metric not found")
				assertGaugeValue(t, m, p.envName, tt.platform, 1)
				assertHasAttribute(t, m, endpointTypeAttrKey, string(tt.endpointType))

				requestFinished()

				// Once the request finishes, it should return to 0 rather than leaving a stray series
				rm, err = p.collectMetrics()
				require.NoError(t, err)
				m = findMetric(rm, connMeasureName)
				require.NotNil(t, m, "active requests metric not found")
				assertGaugeValue(t, m, p.envName, tt.platform, 0)
				assert.Len(t, m.Data.(metricdata.Sum[int64]).DataPoints, 1,
					"increment and decrement should share one attribute set")
			})
		})
	}
}

// The status endpoints and unmatched requests have no environment, so they report the not-provided
// sentinel for both environment.name and platform.category.
func TestActiveRequestMetricsWithoutEnvironment(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		manager, err := NewManager(config.OpenTelemetryConfig{}, time.Minute, slog.Default())
		require.NoError(t, err)
		defer manager.Close()
		manager.SetInstrumentsForTest(p.instruments)

		ri := RequestInfo{Route: "/status", Method: "GET", EndpointType: EndpointTypeStatus}
		requestFinished := StartActiveRequest(manager.GetInstruments(), manager.GetUnscopedEnvironment(), "", ri)
		defer requestFinished()

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, connMeasureName)
		require.NotNil(t, m, "active requests metric not found")
		assertGaugeValue(t, m, notProvidedValue, notProvidedValue, 1)
		assertHasAttribute(t, m, endpointTypeAttrKey, string(EndpointTypeStatus))
	})
}

func assertHasAttribute(t *testing.T, m *metricdata.Metrics, key attribute.Key, expected string) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	for _, dp := range sum.DataPoints {
		val, found := dp.Attributes.Value(key)
		require.True(t, found, "%s attribute not present on %s", key, m.Name)
		assert.Equal(t, expected, val.AsString())
	}
}

func TestRecordRequestDuration(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		RecordRequestDuration(context.Background(), p.instruments, p.env, RequestInfo{UserAgent: userAgentValue, Route: "someRoute", Method: "GET"}, 50*time.Millisecond, ServerDuration)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		dm := findMetric(rm, requestDurationMeasureName)
		require.NotNil(t, dm, "request duration metric not found")
		hist, ok := dm.Data.(metricdata.Histogram[float64])
		require.True(t, ok, "expected Histogram[float64] data")
		require.NotEmpty(t, hist.DataPoints)
		found := false
		for _, dp := range hist.DataPoints {
			routeVal, routeOK := dp.Attributes.Value(httpRouteAttrKey)
			methodVal, methodOK := dp.Attributes.Value(httpRequestMethodAttrKey)
			if routeOK && methodOK && routeVal.AsString() == "someRoute" && methodVal.AsString() == "GET" {
				assert.Equal(t, uint64(1), dp.Count)
				assert.InDelta(t, 0.05, dp.Sum, 0.01, "expected ~50ms duration")
				found = true
			}
		}
		assert.True(t, found, "expected duration data point with route=someRoute, method=GET")
	})
}

func TestRecordEventsReceivedBytes(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		RecordEventsReceivedBytes(context.Background(), p.instruments, p.env, ServerPlatformCategory, RequestInfo{UserAgent: userAgentValue, Route: "/bulk", Method: "POST"}, 1024)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, eventsReceivedMeasureName)
		require.NotNil(t, m, "events received bytes metric not found")
		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok, "expected Sum[int64] data")
		require.NotEmpty(t, sum.DataPoints)
		found := false
		for _, dp := range sum.DataPoints {
			platVal, platOK := dp.Attributes.Value(platformCategoryAttrKey)
			envVal, envOK := dp.Attributes.Value(envNameAttrKey)
			if platOK && envOK && platVal.AsString() == ServerPlatformCategory && envVal.AsString() == p.envName {
				assert.Equal(t, int64(1024), dp.Value)
				found = true
			}
		}
		assert.True(t, found, "expected data point for events received bytes")
	})
}

func TestEventMetricsRecorderViaTestHelper(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		recorder := p.env.NewEventMetricsRecorder(p.instruments)

		recorder.RecordDroppedEvents(5)
		recorder.RecordEventsSent(10)
		recorder.RecordEventsBytesSent(2048)
		recorder.RecordPendingEvents(3)

		rm, err := p.collectMetrics()
		require.NoError(t, err)

		droppedMetric := findMetric(rm, eventsDroppedMeasureName)
		require.NotNil(t, droppedMetric, "events dropped metric not found")

		sentMetric := findMetric(rm, eventsSentMeasureName)
		require.NotNil(t, sentMetric, "events sent metric not found")

		bytesMetric := findMetric(rm, eventsSentSizeMeasureName)
		require.NotNil(t, bytesMetric, "events bytes sent metric not found")

		pendingMetric := findMetric(rm, eventsPendingMeasureName)
		require.NotNil(t, pendingMetric, "events pending metric not found")
	})
}

func TestRecordRequestDurationWithAllAttributes(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		ri := RequestInfo{
			UserAgent:       userAgentValue,
			Route:           "/sdk/eval",
			Method:          "GET",
			URLScheme:       "https",
			ProtocolVersion: "1.1",
			StatusCode:      200,
			ErrorType:       "",
		}
		RecordRequestDuration(context.Background(), p.instruments, p.env, ri, 100*time.Millisecond, ServerDuration)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		dm := findMetric(rm, requestDurationMeasureName)
		require.NotNil(t, dm, "request duration metric not found")
		hist, ok := dm.Data.(metricdata.Histogram[float64])
		require.True(t, ok, "expected Histogram[float64] data")
		require.NotEmpty(t, hist.DataPoints)

		dp := hist.DataPoints[0]
		assert.Equal(t, uint64(1), dp.Count)

		schemeVal, ok := dp.Attributes.Value(urlSchemeAttrKey)
		assert.True(t, ok, "url.scheme attribute missing")
		assert.Equal(t, "https", schemeVal.AsString())

		protoVal, ok := dp.Attributes.Value(networkProtoVersionAttrKey)
		assert.True(t, ok, "network.protocol.version attribute missing")
		assert.Equal(t, "1.1", protoVal.AsString())

		statusVal, ok := dp.Attributes.Value(httpResponseStatusAttrKey)
		assert.True(t, ok, "http.response.status_code attribute missing")
		assert.Equal(t, int64(200), statusVal.AsInt64())
	})
}

func TestRecordRequestDurationWithErrorType(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		ri := RequestInfo{
			UserAgent:  userAgentValue,
			Route:      "/sdk/eval",
			Method:     "GET",
			URLScheme:  "http",
			StatusCode: 500,
			ErrorType:  "500",
		}
		RecordRequestDuration(context.Background(), p.instruments, p.env, ri, 50*time.Millisecond, ServerDuration)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		dm := findMetric(rm, requestDurationMeasureName)
		require.NotNil(t, dm)
		hist, ok := dm.Data.(metricdata.Histogram[float64])
		require.True(t, ok)
		require.NotEmpty(t, hist.DataPoints)

		dp := hist.DataPoints[0]
		errVal, ok := dp.Attributes.Value(errorTypeAttrKey)
		assert.True(t, ok, "error.type attribute missing")
		assert.Equal(t, "500", errVal.AsString())

		statusVal, ok := dp.Attributes.Value(httpResponseStatusAttrKey)
		assert.True(t, ok, "http.response.status_code attribute missing")
		assert.Equal(t, int64(500), statusVal.AsInt64())
	})
}

func TestRecordRequestDurationSkipsWhenMeasureDoesNotRecordDuration(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		// ServerConns has recordDuration: false
		RecordRequestDuration(context.Background(), p.instruments, p.env, RequestInfo{UserAgent: userAgentValue}, 50*time.Millisecond, ServerConns)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		dm := findMetric(rm, requestDurationMeasureName)
		if dm != nil {
			hist, ok := dm.Data.(metricdata.Histogram[float64])
			if ok {
				assert.Empty(t, hist.DataPoints, "expected no duration data points for non-duration measure")
			}
		}
	})
}

func TestRecordEventsFailedSend(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		recorder := p.env.NewEventMetricsRecorder(p.instruments)

		recorder.RecordEventsFailedSend(3, ldevents.EventSendFailureMetadata{StatusCode: 429})

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, eventsSendErrorsMeasureName)
		require.NotNil(t, m, "events send errors metric not found")

		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok, "expected Sum[int64] data")
		require.NotEmpty(t, sum.DataPoints)

		dp := sum.DataPoints[0]
		assert.Equal(t, int64(3), dp.Value)

		// Verify the status_code attribute is an int, not a string
		statusVal, ok := dp.Attributes.Value(statusCodeAttrKey)
		assert.True(t, ok, "status_code attribute missing")
		assert.Equal(t, int64(429), statusVal.AsInt64())
	})
}

func TestRecordEventsFailedSendSkipsZeroCount(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		recorder := p.env.NewEventMetricsRecorder(p.instruments)

		recorder.RecordEventsFailedSend(0, ldevents.EventSendFailureMetadata{StatusCode: 500})

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, eventsSendErrorsMeasureName)
		if m != nil {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if ok {
				for _, dp := range sum.DataPoints {
					assert.Equal(t, int64(0), dp.Value, "expected no data recorded for zero count")
				}
			}
		}
	})
}

func TestWithCountRecordsPolling(t *testing.T) {
	publisher := newTestEventsPublisher()

	manager, err := NewManager(config.OpenTelemetryConfig{}, time.Millisecond*10, slog.Default())
	require.NoError(t, err)
	defer manager.Close()

	env, err := manager.AddEnvironment("polling-test", publisher)
	require.NoError(t, err)

	called := false
	WithCount(env, RequestInfo{UserAgent: "test-agent"}, func() {
		called = true
	}, ServerPollingRequests)

	assert.True(t, called, "function should have been called")

	env.FlushEventsExporter()
	metricsEvent := publisher.expectMetricsEvent(t, time.Second)
	require.Len(t, metricsEvent.PollingCounts, 1)
	assert.Equal(t, int64(1), metricsEvent.PollingCounts[0].Count)
	assert.Equal(t, ServerPlatformCategory, metricsEvent.PollingCounts[0].PlatformCategory)
}

func TestWithCountCallsFunctionWhenEnvNil(t *testing.T) {
	called := false
	WithCount(nil, RequestInfo{UserAgent: "test-agent"}, func() {
		called = true
	}, ServerPollingRequests)
	assert.True(t, called, "function should have been called even with nil env")
}

func TestWithCountCallsFunctionForNonPollingMeasure(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		called := false
		// ServerDuration has recordPolling: false, so no polling metric should be recorded
		WithCount(p.env, RequestInfo{UserAgent: userAgentValue}, func() {
			called = true
		}, ServerDuration)
		assert.True(t, called, "function should have been called")
	})
}

func TestSanitizeTagValue(t *testing.T) {
	assert.Equal(t, "abc", sanitizeTagValue("abc"))
	assert.Equal(t, "not-provided", sanitizeTagValue(""))
	assert.Equal(t, "not-provided", sanitizeTagValue("   "))
	assert.Equal(t, "react_2.0.0", sanitizeTagValue("react/2.0.0"))
}

func TestSanitizeRouteValue(t *testing.T) {
	assert.Equal(t, "/sdk/evalx/contexts/{context}", sanitizeRouteValue("/sdk/evalx/contexts/{context}"))
	assert.Equal(t, "not-provided", sanitizeRouteValue(""))
	assert.Equal(t, "not-provided", sanitizeRouteValue("   "))
}

// Helper functions for asserting OTel metric data

func findMetric(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func assertGaugeValue(t *testing.T, m *metricdata.Metrics, envName, platform string, expected int64) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	found := false
	for _, dp := range sum.DataPoints {
		platVal, platOK := dp.Attributes.Value(platformCategoryAttrKey)
		envVal, envOK := dp.Attributes.Value(envNameAttrKey)
		if platOK && envOK && platVal.AsString() == platform && envVal.AsString() == envName {
			assert.Equal(t, expected, dp.Value, "unexpected value for %s (platform=%s, env=%s)", m.Name, platform, envName)
			found = true
		}
	}
	assert.True(t, found, "no data point found for %s with platform=%s, env=%s", m.Name, platform, envName)
}

// Ignore unused import warning - context is needed for p.collectMetrics
var _ = context.Background
