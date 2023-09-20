package sharedtest

import (
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

type ReceivedItemUpdate struct {
	Kind ldstoretypes.DataKind
	Key  string
	Item ldstoretypes.ItemDescriptor
}

func NewInMemoryStore() subsystems.DataStore {
	store, err := ldcomponents.InMemoryDataStore().Build(subsystems.BasicClientContext{})
	if err != nil {
		panic(err)
	}
	return store
}

func UpsertFlag(store subsystems.DataStore, flag ldmodel.FeatureFlag) (bool, error) {
	return store.Upsert(ldstoreimpl.Features(), flag.Key, FlagDesc(flag))
}

func UpsertSegment(store subsystems.DataStore, segment ldmodel.Segment) (bool, error) {
	return store.Upsert(ldstoreimpl.Segments(), segment.Key, SegmentDesc(segment))
}

func UpsertConfigOverride(store subsystems.DataStore, segment ldmodel.ConfigOverride) (bool, error) {
	return store.Upsert(ldstoreimpl.ConfigOverrides(), segment.Key, ConfigOverrideDesc(segment))
}

func UpsertMetric(store subsystems.DataStore, segment ldmodel.Metric) (bool, error) {
	return store.Upsert(ldstoreimpl.Metrics(), segment.Key, MetricDesc(segment))
}

func FlagDesc(flag ldmodel.FeatureFlag) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag}
}

func SegmentDesc(segment ldmodel.Segment) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: segment.Version, Item: &segment}
}

func ConfigOverrideDesc(override ldmodel.ConfigOverride) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: override.Version, Item: &override}
}

func MetricDesc(metric ldmodel.Metric) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: metric.Version, Item: &metric}
}

func DeletedItem(version int) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: version, Item: nil}
}

func MakeStoreWithData(initialized bool) subsystems.DataStore {
	store := NewInMemoryStore()
	if initialized {
		err := store.Init(AllData)
		if err != nil {
			panic(err)
		}
	}
	return store
}
