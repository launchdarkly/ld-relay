package streams

import (
	"fmt"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
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
