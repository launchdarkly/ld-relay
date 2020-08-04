package sharedtest

import (
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

type ReceivedItemUpdate struct {
	Kind ldstoretypes.DataKind
	Key  string
	Item ldstoretypes.ItemDescriptor
}

type ExistingDataStoreFactory struct {
	Instance interfaces.DataStore
}

func (f ExistingDataStoreFactory) CreateDataStore(
	interfaces.ClientContext,
	interfaces.DataStoreUpdates,
) (interfaces.DataStore, error) {
	return f.Instance, nil
}

func NewInMemoryStore() interfaces.DataStore {
	store, err := ldcomponents.InMemoryDataStore().CreateDataStore(SDKContextImpl{}, nil)
	if err != nil {
		panic(err)
	}
	return store
}

func UpsertFlag(store interfaces.DataStore, flag ldmodel.FeatureFlag) (bool, error) {
	return store.Upsert(ldstoreimpl.Features(), flag.Key, FlagDesc(flag))
}

func UpsertSegment(store interfaces.DataStore, segment ldmodel.Segment) (bool, error) {
	return store.Upsert(ldstoreimpl.Segments(), segment.Key, SegmentDesc(segment))
}

func FlagDesc(flag ldmodel.FeatureFlag) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag}
}

func SegmentDesc(segment ldmodel.Segment) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: segment.Version, Item: &segment}
}

func DeletedItem(version int) ldstoretypes.ItemDescriptor {
	return ldstoretypes.ItemDescriptor{Version: version, Item: nil}
}

func MakeStoreWithData(initialized bool) interfaces.DataStore {
	store := NewInMemoryStore()
	if initialized {
		err := store.Init(AllData)
		if err != nil {
			panic(err)
		}
	}
	return store
}
