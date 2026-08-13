package tracing

import (
	"context"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingExporter records the spans it is given, after the sanitizer has run over them.
type capturingExporter struct {
	spans []sdktrace.ReadOnlySpan
}

func (c *capturingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.spans = append(c.spans, spans...)
	return nil
}

func (c *capturingExporter) Shutdown(context.Context) error { return nil }

// sanitizingProvider builds the pipeline NewTracingProvider builds: a sanitizer wrapping the exporter.
// WithSyncer exports on span end, so the captured spans are ready when the test looks at them.
func sanitizingProvider(t *testing.T, opts ...sdktrace.TracerProviderOption) (trace.Tracer, *capturingExporter) {
	t.Helper()
	capture := &capturingExporter{}
	opts = append(opts, sdktrace.WithSyncer(NewUTF8SanitizingExporter(capture)))
	provider := sdktrace.NewTracerProvider(opts...)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider.Tracer("test"), capture
}

func exportedSpan(t *testing.T, capture *capturingExporter) sdktrace.ReadOnlySpan {
	t.Helper()
	require.Len(t, capture.spans, 1)
	return capture.spans[0]
}

func spanAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) attribute.Value {
	t.Helper()
	for _, kv := range span.Attributes() {
		if kv.Key == attribute.Key(key) {
			return kv.Value
		}
	}
	require.Failf(t, "attribute is missing", "span has no %s attribute", key)
	return attribute.Value{}
}

// A string attribute holding a byte outside UTF-8 fails the OTLP marshal for the whole export batch.
func TestSanitizerRepairsStringAttributes(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	_, span := tracer.Start(context.Background(), "request", trace.WithAttributes(
		attribute.String("server.address", "relay-\xff\xfe.example.com"),
		attribute.String("user_agent.original", "agent-\xff\xfe"),
		attribute.String("url.path", "/status/\xff\xfe"),
	))
	span.End()

	exported := exportedSpan(t, capture)
	assert.Equal(t, "relay-.example.com", spanAttr(t, exported, "server.address").AsString())
	assert.Equal(t, "agent-", spanAttr(t, exported, "user_agent.original").AsString())
	assert.Equal(t, "/status/", spanAttr(t, exported, "url.path").AsString())
}

// The span name carries request data too: the HTTP instrumentation interpolates the request method
// into it for a request that matches no route.
func TestSanitizerRepairsTheSpanName(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	_, span := tracer.Start(context.Background(), "HTTP GET\xff\xfe route not found")
	span.End()

	assert.Equal(t, "HTTP GET route not found", exportedSpan(t, capture).Name())
}

func TestSanitizerRepairsStringSlices(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	_, span := tracer.Start(context.Background(), "request", trace.WithAttributes(
		attribute.StringSlice("custom.values", []string{"clean", "dirty-\xff\xfe", "also-clean"}),
	))
	span.End()

	assert.Equal(t, []string{"clean", "dirty-", "also-clean"},
		spanAttr(t, exportedSpan(t, capture), "custom.values").AsStringSlice())
}

// Every string field of the span protocol has to be covered, not just the attributes, because any one
// of them fails the same marshal.
func TestSanitizerRepairsTheStatusDescription(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	_, span := tracer.Start(context.Background(), "request")
	span.SetStatus(codes.Error, "failed for \xff\xfe")
	span.End()

	assert.Equal(t, "failed for ", exportedSpan(t, capture).Status().Description)
}

func TestSanitizerRepairsEvents(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	_, span := tracer.Start(context.Background(), "request")
	span.AddEvent("event-\xff\xfe", trace.WithAttributes(
		attribute.String("detail", "value-\xff\xfe"),
	))
	span.End()

	events := exportedSpan(t, capture).Events()
	require.Len(t, events, 1)
	assert.Equal(t, "event-", events[0].Name)
	require.Len(t, events[0].Attributes, 1)
	assert.Equal(t, "value-", events[0].Attributes[0].Value.AsString())
}

func TestSanitizerRepairsLinkAttributes(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	linked := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01},
		SpanID:  trace.SpanID{0x02},
	})
	_, span := tracer.Start(context.Background(), "request", trace.WithLinks(trace.Link{
		SpanContext: linked,
		Attributes:  []attribute.KeyValue{attribute.String("detail", "value-\xff\xfe")},
	}))
	span.End()

	links := exportedSpan(t, capture).Links()
	require.Len(t, links, 1)
	require.Len(t, links[0].Attributes, 1)
	assert.Equal(t, "value-", links[0].Attributes[0].Value.AsString())
}

// A span with nothing to repair is passed through untouched, which is the common case.
func TestSanitizerLeavesValidSpansAlone(t *testing.T) {
	tracer, capture := sanitizingProvider(t)

	_, span := tracer.Start(context.Background(), "request", trace.WithAttributes(
		attribute.String("server.address", "relay.example.com"),
		attribute.Int("server.port", 8030),
		attribute.Bool("custom.flag", true),
	))
	span.End()

	exported := exportedSpan(t, capture)
	assert.Equal(t, "request", exported.Name())
	assert.Equal(t, "relay.example.com", spanAttr(t, exported, "server.address").AsString())
	assert.Equal(t, int64(8030), spanAttr(t, exported, "server.port").AsInt64())
	assert.Len(t, exported.Attributes(), 3)
}

// Repairing one span in a batch must not disturb the others, and every span still has to be exported.
func TestSanitizerRepairsOneSpanInABatchWithoutLosingTheRest(t *testing.T) {
	capture := &capturingExporter{}
	exporter := NewUTF8SanitizingExporter(capture)

	// Spans are built through a plain provider, so they arrive at the sanitizer unrepaired.
	source := &capturingExporter{}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(source))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	makeSpan := func(address string) sdktrace.ReadOnlySpan {
		before := len(source.spans)
		_, span := provider.Tracer("test").Start(context.Background(), "request",
			trace.WithAttributes(attribute.String("server.address", address)))
		span.End()
		require.Len(t, source.spans, before+1)
		return source.spans[before]
	}

	batch := []sdktrace.ReadOnlySpan{
		makeSpan("clean-one.example.com"),
		makeSpan("poisoned-\xff\xfe.example.com"),
		makeSpan("clean-two.example.com"),
	}
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))

	require.Len(t, capture.spans, 3)
	assert.Equal(t, "clean-one.example.com", spanAttr(t, capture.spans[0], "server.address").AsString())
	assert.Equal(t, "poisoned-.example.com", spanAttr(t, capture.spans[1], "server.address").AsString())
	assert.Equal(t, "clean-two.example.com", spanAttr(t, capture.spans[2], "server.address").AsString())

	// The caller's batch is reused by the batch processor, so it must not have been rewritten.
	assert.Equal(t, "poisoned-\xff\xfe.example.com",
		spanAttr(t, batch[1], "server.address").AsString())
}

// Repairing an attribute must not leave any invalid value behind, at any attribute count limit.
func TestSanitizerRepairsUnderAnAttributeCountLimit(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 6, 128, -1} {
		t.Run("limit", func(t *testing.T) {
			tracer, capture := sanitizingProvider(t, sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{
				AttributeCountLimit:         limit,
				AttributeValueLengthLimit:   -1,
				EventCountLimit:             128,
				LinkCountLimit:              128,
				AttributePerEventCountLimit: 128,
				AttributePerLinkCountLimit:  128,
			}))

			_, span := tracer.Start(context.Background(), "request", trace.WithAttributes(
				attribute.String("server.address", "relay-\xff\xfe.example.com"),
				attribute.String("user_agent.original", "agent-\xff\xfe"),
				attribute.String("client.address", "1.2.3.4\xff\xfe"),
			))
			span.End()

			for _, kv := range exportedSpan(t, capture).Attributes() {
				if kv.Value.Type() != attribute.STRING {
					continue
				}
				assert.True(t, utf8.ValidString(kv.Value.AsString()),
					"limit=%d: %s kept an invalid value %q", limit, kv.Key, kv.Value.AsString())
			}
		})
	}
}

// Shutdown has to reach the exporter the sanitizer wraps.
func TestSanitizingExporterForwardsShutdown(t *testing.T) {
	capture := &shutdownRecorder{}
	require.NoError(t, NewUTF8SanitizingExporter(capture).Shutdown(context.Background()))
	assert.True(t, capture.shutdown)
}

type shutdownRecorder struct {
	shutdown bool
}

func (s *shutdownRecorder) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }

func (s *shutdownRecorder) Shutdown(context.Context) error {
	s.shutdown = true
	return nil
}
