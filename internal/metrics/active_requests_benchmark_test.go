package metrics

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// A request-shaped RequestInfo, with every attribute populated the way a real SDK request would
// populate it, so the attribute set being built is representative in size.
func benchRequestInfo() RequestInfo {
	return RequestInfo{
		UserAgent:          "GoClient/7.15.4",
		SDKWrapper:         "flutter-client/2.0.0",
		Route:              "/sdk/evalx/{envId}/contexts/{context}",
		Method:             "GET",
		ApplicationID:      "my-app",
		ApplicationVersion: "1.2.3",
		EndpointType:       EndpointTypePoll,
		URLScheme:          "https",
		ProtocolVersion:    "1.1",
	}
}

func benchEnvironmentManager(b *testing.B) *EnvironmentManager {
	b.Helper()
	return &EnvironmentManager{
		envKVs: []attribute.KeyValue{
			envNameAttrKey.String("bench-env"),
		},
	}
}

// BenchmarkStartActiveRequest measures the per-request cost of the recording site: one attribute set
// built, one increment/decrement pair on the active-request UpDownCounter, and one increment on the
// cumulative request Counter. Duration recording already built a comparable set, so this is the
// marginal cost of these two instruments, not the total cost of instrumenting a request.
//
// The three measurements share one attribute set, so the counter costs an instrument Add rather than
// another set construction, which is what dominates here. Measured at roughly 80 ns/op against a real
// meter, plus one 16-byte allocation for the extra call's variadic option slice. That allocation is
// paid whether or not OpenTelemetry is enabled; the time is not, because a noop Add does no work.
//
// The noop case matters as much as the real one: an operator with OpenTelemetry disabled still pays
// for the attribute set, because the instruments are noop but the attributes are built before the
// instrument is touched.
func BenchmarkStartActiveRequest(b *testing.B) {
	ri := benchRequestInfo()
	em := benchEnvironmentManager(b)

	b.Run("real meter", func(b *testing.B) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		instruments, err := NewInstrumentsForTest(provider.Meter("ld-relay"))
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			StartActiveRequest(instruments, em, ri)()
		}
	})

	b.Run("noop meter", func(b *testing.B) {
		instruments, err := NewInstrumentsForTest(noop.Meter{})
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			StartActiveRequest(instruments, em, ri)()
		}
	})
}

// BenchmarkBuildRequestAttributes isolates the attribute set construction, which dominates the cost
// above. At eight attributes this stays inside attribute.NewSet's fixed-size fast path (ten or fewer);
// it was twelve before platform.category, sdk.wrapper, instance.id and relay.id came off.
func BenchmarkBuildRequestAttributes(b *testing.B) {
	ri := benchRequestInfo()
	em := benchEnvironmentManager(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildRequestAttributes(em.envKVs, ri)
	}
}
