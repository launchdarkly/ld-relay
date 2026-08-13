package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// These benchmarks measure the sanitizer's own cost. They use WithSyncer so that the exporter runs on
// the benchmark's goroutine and its cost is actually counted. Relay uses WithBatcher, which calls the
// exporter from its own goroutine, so in production no request waits for any of this.
//
// A realistic request span attribute set, matching what otelmux records: mostly valid strings, a
// couple of ints. The common case is that nothing needs repair, so that is what this measures.
func benchAttrs() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("server.address", "relay.example.com"),
		attribute.Int("server.port", 8030),
		attribute.String("http.request.method", "GET"),
		attribute.String("url.scheme", "http"),
		attribute.String("url.path", "/sdk/evalx/contexts/REDACTED"),
		attribute.String("user_agent.original", "GoClient/7.15.4"),
		attribute.String("client.address", "10.1.2.3"),
		attribute.String("network.peer.address", "10.1.2.3"),
		attribute.Int("network.peer.port", 54321),
		attribute.String("network.protocol.version", "1.1"),
		attribute.String("http.route", "/sdk/evalx/contexts/{context}"),
		attribute.String("http.request.method_original", "REPORT"),
	}
}

type discardExporter struct{}

func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (discardExporter) Shutdown(context.Context) error                             { return nil }

func benchmarkSpanStart(b *testing.B, withSanitizer bool) {
	var exporter sdktrace.SpanExporter = discardExporter{}
	if withSanitizer {
		exporter = NewUTF8SanitizingExporter(exporter)
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithSyncer(exporter)}
	provider := sdktrace.NewTracerProvider(opts...)
	b.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("bench")
	attrs := benchAttrs()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, span := tracer.Start(ctx, "GET /sdk/evalx/contexts/{context}", trace.WithAttributes(attrs...))
		span.End()
	}
}

func BenchmarkSpanStartWithoutSanitizer(b *testing.B) { benchmarkSpanStart(b, false) }
func BenchmarkSpanStartWithSanitizer(b *testing.B)    { benchmarkSpanStart(b, true) }

// The repair path, for contrast: one poisoned attribute out of twelve.
func BenchmarkSpanStartWithSanitizerRepairing(b *testing.B) {
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(NewUTF8SanitizingExporter(discardExporter{})),
	)
	b.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("bench")
	attrs := benchAttrs()
	attrs[0] = attribute.String("server.address", "relay-\xff\xfe.example.com")
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, span := tracer.Start(ctx, "GET /sdk/evalx/contexts/{context}", trace.WithAttributes(attrs...))
		span.End()
	}
}
