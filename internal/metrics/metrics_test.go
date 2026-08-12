package metrics

import (
	"context"
	"log/slog"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/launchdarkly/ld-relay/v9/config"

	ct "github.com/launchdarkly/go-configtypes"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
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
		StartActiveRequest(manager.GetInstruments(), em, ri)()
		StartActiveRequest(manager.GetInstruments(), manager.GetUnscopedEnvironment(), ri)()
		RecordRequestDuration(context.Background(), manager.GetInstruments(), em, ri, time.Millisecond)
		RecordEventsReceivedBytes(context.Background(), manager.GetInstruments(), em, ri, 100)

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

// The cardinality limit is only meaningful through the MeterProvider it is applied to, so these
// tests record more distinct attribute sets than the limit allows and check what actually comes
// back out of a reader.
func recordDistinctAttributeSets(t *testing.T, otlpConfig config.OpenTelemetryConfig, count int) metricdata.Sum[int64] {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	opts := append([]sdkmetric.Option{sdkmetric.WithReader(reader)},
		cardinalityLimitOptions(otlpConfig, slog.Default())...)
	meterProvider := sdkmetric.NewMeterProvider(opts...)
	defer func() {
		require.NoError(t, meterProvider.Shutdown(context.Background()))
	}()

	counter, err := meterProvider.Meter("ld-relay").Int64Counter("test.counter")
	require.NoError(t, err)
	for i := range count {
		counter.Add(context.Background(), 1, otelmetric.WithAttributes(attribute.Int("index", i)))
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	require.Len(t, rm.ScopeMetrics, 1)
	require.Len(t, rm.ScopeMetrics[0].Metrics, 1)

	sum, ok := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	return sum
}

func hasOverflowDataPoint(sum metricdata.Sum[int64]) bool {
	for _, dp := range sum.DataPoints {
		if value, ok := dp.Attributes.Value(attribute.Key("otel.metric.overflow")); ok && value.AsBool() {
			return true
		}
	}
	return false
}

func TestConfiguredCardinalityLimitIsAppliedToTheMeterProvider(t *testing.T) {
	otlpConfig := config.OpenTelemetryConfig{Enabled: true, MetricsCardinalityLimit: ct.NewOptInt(3)}

	sum := recordDistinctAttributeSets(t, otlpConfig, 10)

	// The SDK reserves one of the allotted series for the overflow data point, so a limit of 3
	// yields two real series plus the overflow.
	assert.Len(t, sum.DataPoints, 3)
	assert.True(t, hasOverflowDataPoint(sum))
}

func TestCardinalityLimitOfZeroRemovesTheLimit(t *testing.T) {
	otlpConfig := config.OpenTelemetryConfig{Enabled: true, MetricsCardinalityLimit: ct.NewOptInt(0)}

	sum := recordDistinctAttributeSets(t, otlpConfig, 10)

	assert.Len(t, sum.DataPoints, 10)
	assert.False(t, hasOverflowDataPoint(sum))
}

// Leaving the setting undefined has to mean "pass no option", so that the SDK default and its own
// OTEL_GO_X_CARDINALITY_LIMIT still decide the limit.
func TestUnconfiguredCardinalityLimitAddsNoOption(t *testing.T) {
	assert.Empty(t, cardinalityLimitOptions(config.OpenTelemetryConfig{Enabled: true}, slog.Default()))
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
	for _, endpointType := range []EndpointType{EndpointTypeStream, EndpointTypePoll, EndpointTypeEvents} {
		t.Run(string(endpointType), func(t *testing.T) {
			testWithOTel(t, func(p testWithOTelParams) {
				ri := RequestInfo{UserAgent: userAgentValue, Route: "/test", Method: "GET", EndpointType: endpointType}
				requestFinished := StartActiveRequest(p.instruments, p.env, ri)

				// While the request is in flight, the count should be 1
				rm, err := p.collectMetrics()
				require.NoError(t, err)
				m := findMetric(rm, connMeasureName)
				require.NotNil(t, m, "active requests metric not found")
				assertGaugeValue(t, m, p.envName, 1)
				assertHasAttribute(t, m, endpointTypeAttrKey, string(endpointType))

				requestFinished()

				// Once the request finishes, it should return to 0 rather than leaving a stray series
				rm, err = p.collectMetrics()
				require.NoError(t, err)
				m = findMetric(rm, connMeasureName)
				require.NotNil(t, m, "active requests metric not found")
				assertGaugeValue(t, m, p.envName, 0)
				assert.Len(t, m.Data.(metricdata.Sum[int64]).DataPoints, 1,
					"increment and decrement should share one attribute set")
			})
		})
	}
}

// The status endpoints and unmatched requests have no environment, so they report the not-provided
// sentinel for environment.name.
func TestActiveRequestMetricsWithoutEnvironment(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		manager, err := NewManager(config.OpenTelemetryConfig{}, time.Minute, slog.Default())
		require.NoError(t, err)
		defer manager.Close()
		manager.SetInstrumentsForTest(p.instruments)

		ri := RequestInfo{Route: "/status", Method: "GET", EndpointType: EndpointTypeStatus}
		requestFinished := StartActiveRequest(manager.GetInstruments(), manager.GetUnscopedEnvironment(), ri)
		defer requestFinished()

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, connMeasureName)
		require.NotNil(t, m, "active requests metric not found")
		assertGaugeValue(t, m, notProvidedValue, 1)
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
		RecordRequestDuration(context.Background(), p.instruments, p.env, RequestInfo{UserAgent: userAgentValue, Route: "someRoute", Method: "GET"}, 50*time.Millisecond)

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
		RecordEventsReceivedBytes(context.Background(), p.instruments, p.env, RequestInfo{UserAgent: userAgentValue, Route: "/bulk", Method: "POST"}, 1024)

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, eventsReceivedMeasureName)
		require.NotNil(t, m, "events received bytes metric not found")
		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok, "expected Sum[int64] data")
		require.NotEmpty(t, sum.DataPoints)
		found := false
		for _, dp := range sum.DataPoints {
			envVal, envOK := dp.Attributes.Value(envNameAttrKey)
			if envOK && envVal.AsString() == p.envName {
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
		RecordRequestDuration(context.Background(), p.instruments, p.env, ri, 100*time.Millisecond)

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
		RecordRequestDuration(context.Background(), p.instruments, p.env, ri, 50*time.Millisecond)

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

		// Verify the status code attribute is an int, not a string
		statusVal, ok := dp.Attributes.Value(httpResponseStatusAttrKey)
		assert.True(t, ok, "http.response.status_code attribute missing")
		assert.Equal(t, int64(429), statusVal.AsInt64())

		// Every measurement on this instrument is a failure, so error.type is always present. With a
		// response, semconv reports the status code as its value.
		errorVal, ok := dp.Attributes.Value(errorTypeAttrKey)
		assert.True(t, ok, "error.type attribute missing")
		assert.Equal(t, "429", errorVal.AsString())
	})
}

// A send that failed before any response reports 0 in the metadata. Publishing that as a status code
// would invent a response that never arrived, so the status code is omitted and the failure is reported
// through the unclassified error.type value instead.
func TestRecordEventsFailedSendWithNoResponse(t *testing.T) {
	testWithOTel(t, func(p testWithOTelParams) {
		recorder := p.env.NewEventMetricsRecorder(p.instruments)

		recorder.RecordEventsFailedSend(2, ldevents.EventSendFailureMetadata{StatusCode: 0})

		rm, err := p.collectMetrics()
		require.NoError(t, err)
		m := findMetric(rm, eventsSendErrorsMeasureName)
		require.NotNil(t, m, "events send errors metric not found")

		sum, ok := m.Data.(metricdata.Sum[int64])
		require.True(t, ok, "expected Sum[int64] data")
		require.NotEmpty(t, sum.DataPoints)

		dp := sum.DataPoints[0]
		assert.Equal(t, int64(2), dp.Value)

		_, ok = dp.Attributes.Value(httpResponseStatusAttrKey)
		assert.False(t, ok, "no response was received, so no status code should be reported")

		errorVal, ok := dp.Attributes.Value(errorTypeAttrKey)
		require.True(t, ok, "error.type attribute missing")
		assert.Equal(t, "_OTHER", errorVal.AsString())
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
		// ServerConns has recordPolling: false, so no polling metric should be recorded
		WithCount(p.env, RequestInfo{UserAgent: userAgentValue}, func() {
			called = true
		}, ServerConns)
		assert.True(t, called, "function should have been called")
	})
}

func TestSanitizeTagValue(t *testing.T) {
	assert.Equal(t, "abc", sanitizeTagValue("abc"))
	assert.Equal(t, "not-provided", sanitizeTagValue(""))
	assert.Equal(t, "not-provided", sanitizeTagValue("   "))
	assert.Equal(t, "react_2.0.0", sanitizeTagValue("react/2.0.0"))
}

func TestSanitizeVerbatimValue(t *testing.T) {
	assert.Equal(t, "abc", sanitizeVerbatimValue("abc"))
	assert.Equal(t, "not-provided", sanitizeVerbatimValue(""))
	assert.Equal(t, "not-provided", sanitizeVerbatimValue("   "))
	assert.Equal(t, "react/2.0.0", sanitizeVerbatimValue("react/2.0.0"))
	assert.Equal(t, "Node/3.4.0", sanitizeVerbatimValue("Node\xff/3.4.0"))
}

// The user agent is reported under the semantic-convention key, with the value the client sent. The
// tracing instrumentation records the same attribute on the request span from the same header, so a
// mangled value here would stop metrics and traces being joined on it.
func TestUserAgentUsesSemconvKeyAndVerbatimValue(t *testing.T) {
	attrs := buildRequestAttributes(nil, RequestInfo{UserAgent: "Node/3.4.0"})

	value, ok := attrs.Value(attribute.Key("user_agent.original"))
	require.True(t, ok, "user_agent.original attribute not present")
	assert.Equal(t, "Node/3.4.0", value.AsString())

	_, ok = attrs.Value(attribute.Key("user_agent"))
	assert.False(t, ok, "the bare user_agent key shadows the semconv user_agent namespace")
}

// Attribute values are serialized into OTLP protobuf string fields, which proto3 requires to be valid
// UTF-8. One bad byte fails the marshal for the whole export batch, and the poisoned series is
// cumulative, so exports keep failing until restart. Header values are not restricted to ASCII, so this
// has to be handled here rather than assumed away.
func TestSanitizeTagValueStripsInvalidUTF8(t *testing.T) {
	specs := []struct {
		name  string
		input string
		want  string
	}{
		{name: "invalid bytes in the middle", input: "bad-\xff\xfe-agent", want: "bad--agent"},
		{name: "leading invalid byte", input: "\xffGoClient", want: "GoClient"},
		{name: "entirely invalid collapses to sentinel", input: "\xff\xfe", want: notProvidedValue},
		{name: "invalid plus a slash", input: "Node\xff/3.4.0", want: "Node_3.4.0"},
		{name: "valid multi-byte UTF-8 is preserved", input: "Ruby-\u00e9", want: "Ruby-\u00e9"},
		{name: "valid ASCII is untouched", input: "GoClient", want: "GoClient"},
	}

	for _, tt := range specs {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTagValue(tt.input)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "sanitized value must be valid UTF-8, got %q", got)
		})
	}
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

func assertGaugeValue(t *testing.T, m *metricdata.Metrics, envName string, expected int64) {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data for %s", m.Name)
	found := false
	for _, dp := range sum.DataPoints {
		envVal, envOK := dp.Attributes.Value(envNameAttrKey)
		if envOK && envVal.AsString() == envName {
			assert.Equal(t, expected, dp.Value, "unexpected value for %s (env=%s)", m.Name, envName)
			found = true
		}
	}
	assert.True(t, found, "no data point found for %s with env=%s", m.Name, envName)
}

// Ignore unused import warning - context is needed for p.collectMetrics
var _ = context.Background
