package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNewManagerWithNoExporters(t *testing.T) {
	manager, err := NewManager(config.MetricsConfig{}, 0, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	defer manager.Close()

	assert.NotNil(t, manager.instruments)
}

func TestNewManagerReturnsInstruments(t *testing.T) {
	manager, err := NewManager(config.MetricsConfig{}, 0, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	defer manager.Close()

	instruments := manager.GetInstruments()
	assert.NotNil(t, instruments)
	assert.NotNil(t, instruments.connections)
	assert.NotNil(t, instruments.newConnections)
	assert.NotNil(t, instruments.requests)
	assert.NotNil(t, instruments.tracer)
}

func TestAddEnvironmentWithoutEventPublisher(t *testing.T) {
	manager, err := NewManager(config.MetricsConfig{}, 0, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	defer manager.Close()

	env, err := manager.AddEnvironment("name", nil)

	assert.NoError(t, err)
	require.NotNil(t, env)
	assert.NotEqual(t, attribute.Set{}, env.GetAttributes())
}

func TestAddEnvironmentWithEventPublisher(t *testing.T) {
	publisher := newTestEventsPublisher()

	manager, err := NewManager(config.MetricsConfig{}, time.Millisecond*10, ldlog.NewDisabledLoggers())
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
	manager, err := NewManager(config.MetricsConfig{}, 0, ldlog.NewDisabledLoggers())
	require.NoError(t, err)
	manager.Close()
	env, err := manager.AddEnvironment("name", nil)
	assert.Nil(t, env)
	assert.Error(t, err)
}

func TestRemoveEnvironment(t *testing.T) {
	manager, err := NewManager(config.MetricsConfig{}, 0, ldlog.NewDisabledLoggers())
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

func TestConnectionMetrics(t *testing.T) {
	specs := []struct {
		platform string
		measure  Measure
	}{
		{platform: BrowserPlatformCategory, measure: BrowserConns},
		{platform: MobilePlatformCategory, measure: MobileConns},
		{platform: ServerPlatformCategory, measure: ServerConns},
	}

	for _, tt := range specs {
		t.Run(tt.platform, func(t *testing.T) {
			testWithOTel(t, func(p testWithOTelParams) {
				WithGauge(p.env, p.instruments, userAgentValue, "", func() {
					// While the gauge is active, check that the connection count is 1
					rm, err := p.collectMetrics()
					require.NoError(t, err)
					m := findMetric(rm, connMeasureName)
					require.NotNil(t, m, "connections metric not found")
					assertGaugeValue(t, m, p.envName, tt.platform, 1)
				}, tt.measure)

				// After the gauge function returns, check that the connection count is 0
				rm, err := p.collectMetrics()
				require.NoError(t, err)
				m := findMetric(rm, connMeasureName)
				require.NotNil(t, m, "connections metric not found")
				assertGaugeValue(t, m, p.envName, tt.platform, 0)
			})
		})
	}
}

func TestNewConnectionMetrics(t *testing.T) {
	specs := []struct {
		platform string
		measure  Measure
	}{
		{platform: BrowserPlatformCategory, measure: NewBrowserConns},
		{platform: MobilePlatformCategory, measure: NewMobileConns},
		{platform: ServerPlatformCategory, measure: NewServerConns},
	}

	for _, tt := range specs {
		t.Run(tt.platform, func(t *testing.T) {
			testWithOTel(t, func(p testWithOTelParams) {
				WithCount(p.env, p.instruments, userAgentValue, "", func() {}, tt.measure)

				rm, err := p.collectMetrics()
				require.NoError(t, err)
				m := findMetric(rm, newConnMeasureName)
				require.NotNil(t, m, "newconnections metric not found")
				assertCounterValue(t, m, p.envName, tt.platform, 1)
			})
		})
	}
}

func TestWithRouteCount(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		WithRouteCount(context.Background(), p.env, p.instruments, userAgentValue, "", "someRoute", "GET", func() {}, ServerRequests)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, requestMeasureName)
		require.NotNil(t, m, "requests metric not found")

		// Verify the data has a data point with route and method attributes
		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok, "expected Sum[int64] data")
		require.NotEmpty(t, sum.DataPoints)
		found := false
		for _, dp := range sum.DataPoints {
			routeVal, routeOK := dp.Attributes.Value(routeAttrKey)
			methodVal, methodOK := dp.Attributes.Value(methodAttrKey)
			if routeOK && methodOK && routeVal.AsString() == "someRoute" && methodVal.AsString() == "GET" {
				assert.Equal(t, int64(1), dp.Value)
				found = true
			}
		}
		assert.True(t, found, "expected data point with route=someRoute, method=GET")

		// Verify span was created
		spans := p.spanExporter.GetSpans()
		require.NotEmpty(t, spans)
		assert.Equal(t, "someRoute", spans[0].Name)
	})
}

func TestSanitizeTagValue(t *testing.T) {
	assert.Equal(t, "abc", sanitizeTagValue("abc"))
	assert.Equal(t, "not-provided", sanitizeTagValue(""))
	assert.Equal(t, "not-provided", sanitizeTagValue("   "))
	assert.Equal(t, "react_2.0.0", sanitizeTagValue("react/2.0.0"))
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

func assertCounterValue(t *testing.T, m *metricdata.Metrics, envName, platform string, expected int64) {
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
