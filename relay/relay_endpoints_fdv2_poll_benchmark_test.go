package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// makeLargePollingDataSet builds a data set big enough that payload serialization dominates
// the polling handlers' cost. The flag shape mirrors the internal/streams encoding benchmarks:
// each flag has rules and user targets, and is client-side visible so the eval endpoint
// includes it.
func makeLargePollingDataSet(numFlags, numSegments int) []ldstoretypes.Collection {
	numRules := 20
	numTargets := 2
	numUsersInTarget := 20

	allData := []ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features()},
		{Kind: ldstoreimpl.Segments()},
	}

	for i := 0; i < numFlags; i++ {
		fb := ldbuilders.NewFlagBuilder(fmt.Sprintf("flag%d", i)).Version(1).On(true).
			SingleVariation(ldvalue.String(fmt.Sprintf("value%d", i))).
			ClientSideUsingEnvironmentID(true)
		for r := 0; r < numRules; r++ {
			fb.AddRule(ldbuilders.NewRuleBuilder().ID(fmt.Sprintf("rule%d", r)).Variation(0))
		}
		for t := 0; t < numTargets; t++ {
			var userKeys []string
			for u := 0; u < numUsersInTarget; u++ {
				userKeys = append(userKeys, fmt.Sprintf("user%d", u))
			}
			fb.AddTarget(t, userKeys...)
		}
		flag := fb.Build()
		allData[0].Items = append(allData[0].Items,
			ldstoretypes.KeyedItemDescriptor{Key: flag.Key, Item: sharedtest.FlagDesc(flag)})
	}

	for i := 0; i < numSegments; i++ {
		var userKeys []string
		for u := 0; u < numUsersInTarget; u++ {
			userKeys = append(userKeys, fmt.Sprintf("user%d", u))
		}
		segment := ldbuilders.NewSegmentBuilder(fmt.Sprintf("segment%d", i)).Version(1).
			Included(userKeys...).Build()
		allData[1].Items = append(allData[1].Items,
			ldstoretypes.KeyedItemDescriptor{Key: segment.Key, Item: sharedtest.SegmentDesc(segment)})
	}

	return allData
}

func benchmarkPollingEnv(numFlags int) relayenv.EnvContext {
	store := testclient.NewFakeStore(makeLargePollingDataSet(numFlags, numFlags/10))
	return testenv.NewTestEnvContextWithClientFactory("",
		testclient.FakeLDClientFactoryWithStore(true, store), nil)
}

func BenchmarkPollHandlerV2(b *testing.B) {
	for _, numFlags := range []int{100, 2000} {
		b.Run(fmt.Sprintf("flags=%d", numFlags), func(b *testing.B) {
			env := benchmarkPollingEnv(numFlags)
			req := buildPreRoutedRequest("GET", nil, nil, nil, env)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				resp := httptest.NewRecorder()
				pollHandlerV2(resp, req)
				if resp.Code != http.StatusOK {
					b.Fatalf("unexpected status %d", resp.Code)
				}
			}
		})
	}
}

func BenchmarkPollEvalHandlerV2(b *testing.B) {
	contextJSON := []byte(`{"kind": "user", "key": "user-key", "name": "name"}`)
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")

	for _, numFlags := range []int{100, 2000} {
		b.Run(fmt.Sprintf("flags=%d", numFlags), func(b *testing.B) {
			env := benchmarkPollingEnv(numFlags)
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// The request must be rebuilt each iteration because the handler consumes the body.
				req := buildPreRoutedRequest("REPORT", contextJSON, headers, nil, env)
				resp := httptest.NewRecorder()
				pollEvalHandlerV2Shared(resp, req, ct.OptBase2Bytes{})
				if resp.Code != http.StatusOK {
					b.Fatalf("unexpected status %d", resp.Code)
				}
			}
		})
	}
}
