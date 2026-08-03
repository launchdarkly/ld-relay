package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/lduser"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// BenchmarkEvaluateAllFlagsTracedConcurrent measures the evaluation endpoint with tracing
// enabled and requests in flight concurrently, which is where tracer-lookup cost lives:
// resolving a tracer takes the provider's exclusive mutex, so every lookup is a point where
// concurrent requests serialize against each other.
//
// Be careful reading it. The handler spends microseconds between lookups, so the lock is only
// lightly contended and a saved lookup is worth tens of nanoseconds -- around 2% here, close
// to this benchmark's run-to-run spread. It is here to exercise the concurrent traced path,
// not to gate on a number.
func BenchmarkEvaluateAllFlagsTracedConcurrent(b *testing.B) {
	numFlags := 50

	allData := []ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{}},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{}},
	}
	for i := 0; i < numFlags; i++ {
		flag := ldbuilders.NewFlagBuilder(fmt.Sprintf("flag%d", i)).Version(1).
			SingleVariation(ldvalue.String(fmt.Sprintf("value%d", i))).
			ClientSideUsingEnvironmentID(true).
			Build()
		allData[0].Items = append(allData[0].Items,
			ldstoretypes.KeyedItemDescriptor{Key: flag.Key, Item: sharedtest.FlagDesc(flag)})
	}

	user := lduser.NewUserBuilder("user-key").Name("name").Email("email").Custom("a", ldvalue.String("b")).Build()

	store := sharedtest.NewInMemoryStore()
	store.Init(allData)
	ctx := testenv.NewTestEnvContext("", false, store)
	userData := []byte(user.String())
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")

	// A real SDK tracer provider, so tracer lookups take the same lock they take in production.
	// Spans are dropped by the sampler, isolating the lookup cost from export cost.
	previous := otel.GetTracerProvider()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(provider)
	b.Cleanup(func() { otel.SetTracerProvider(previous) })

	handler := evaluateAllFeatureFlags(basictypes.JSClientSDK, ct.OptBase2Bytes{})

	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := buildPreRoutedRequest("REPORT", userData, headers, nil, ctx)
			handler(httptest.NewRecorder(), req)
		}
	})
}
