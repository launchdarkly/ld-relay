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
		InstanceID:         "instance-abc",
		EndpointType:       EndpointTypePoll,
		URLScheme:          "https",
		ProtocolVersion:    "1.1",
	}
}

func benchEnvironmentManager(b *testing.B) *EnvironmentManager {
	b.Helper()
	return &EnvironmentManager{
		envKVs: []attribute.KeyValue{
			relayIDAttrKey.String("bench-relay-id"),
			envNameAttrKey.String("bench-env"),
		},
	}
}

// BenchmarkStartActiveRequest measures the work this change adds to every non-streaming request:
// one attribute set built and one increment/decrement pair on the UpDownCounter. Duration recording
// already built a comparable set before this change, so this is the marginal cost, not the total.
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
			StartActiveRequest(instruments, em, ServerPlatformCategory, ri)()
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
			StartActiveRequest(instruments, em, ServerPlatformCategory, ri)()
		}
	})
}

// BenchmarkBuildRequestAttributes isolates the attribute set construction -- the allocation and sort
// of ~12 attributes that dominates the cost above.
func BenchmarkBuildRequestAttributes(b *testing.B) {
	ri := benchRequestInfo()
	em := benchEnvironmentManager(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildRequestAttributes(em.envKVs, ServerPlatformCategory, ri)
	}
}
