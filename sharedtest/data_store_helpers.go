package sharedtest

import (
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

func NewInMemoryStore() interfaces.DataStore {
	store, err := ldcomponents.InMemoryDataStore().CreateDataStore(SDKContextImpl{}, nil)
	if err != nil {
		panic(err)
	}
	return store
}

func UpsertFlag(store interfaces.DataStore, flag ldmodel.FeatureFlag) (bool, error) {
	return store.Upsert(interfaces.DataKindFeatures(), flag.Key,
		interfaces.StoreItemDescriptor{Version: flag.Version, Item: &flag})
}

func UpsertSegment(store interfaces.DataStore, segment ldmodel.Segment) (bool, error) {
	return store.Upsert(interfaces.DataKindSegments(), segment.Key,
		interfaces.StoreItemDescriptor{Version: segment.Version, Item: &segment})
}
