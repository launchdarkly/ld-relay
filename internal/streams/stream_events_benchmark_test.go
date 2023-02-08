package streams

import (
	"fmt"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"
)

var benchmarkStringResult string // prevents computations from being optimized out of existence

func BenchmarkSerializePutEventWithManyFlags(b *testing.B) {
	allData := makeLargePutDataSet()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		event := MakeServerSidePutEvent(allData)
		benchmarkStringResult = event.Data()
	}
}

func makeLargePutDataSet() []ldstoretypes.Collection {
	numFlags := 50
	numRules := 20
	numTargets := 2
	numUsersInTarget := 20
	allData := []ldstoretypes.Collection{
		{
			Kind:  ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{},
		},
		{
			Kind:  ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{},
		},
	}

	for i := 0; i < numFlags; i++ {
		fb := ldbuilders.NewFlagBuilder(fmt.Sprintf("flag%d", i)).Version(1).On(true)
		for r := 0; r < numRules; r++ {
			rule := ldbuilders.NewRuleBuilder().ID(fmt.Sprintf("rule%d", r))
			fb.AddRule(rule)
		}
		for t := 0; t < numTargets; t++ {
			var userKeys []string
			for u := 0; u < numUsersInTarget; u++ {
				userKeys = append(userKeys, fmt.Sprintf("user%d", u))
			}
			fb.AddTarget(t, userKeys...)
		}
		flag := fb.Build()
		allData[0].Items = append(allData[0].Items, ldstoretypes.KeyedItemDescriptor{flag.Key, sharedtest.FlagDesc(flag)})
	}

	return allData
}
