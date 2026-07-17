package streams

import (
	"fmt"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-jsonstream/v4/jwriter"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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

// Mirrors the work done by getReplayEventsV2 for a full data transfer: each item is
// serialized to JSON, wrapped in a Change, and then the whole set is turned into
// SSE events.
func BenchmarkMakeEventsForSetBasisWithManyFlags(b *testing.B) {
	allData := makeLargePutDataSet()
	selector := subsystems.NewSelector("benchmark-state", 1)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var changes []subsystems.Change
		for _, coll := range allData {
			objectKind := subsystems.FlagKind
			if coll.Kind == ldstoreimpl.Segments() {
				objectKind = subsystems.SegmentKind
			}
			for _, item := range coll.Items {
				writer := jwriter.NewWriter()
				serializeItem(coll.Kind, item.Item, &writer)
				changes = append(changes, subsystems.Change{
					Action:  subsystems.ChangeTypePut,
					Kind:    objectKind,
					Key:     item.Key,
					Version: item.Item.Version,
					Object:  writer.Bytes(),
				})
			}
		}
		for _, event := range MakeEventsForSetBasis(changes, selector) {
			benchmarkStringResult = event.Data()
		}
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
