package sharedtest

import (
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

func NewInMemoryStore() interfaces.DataStore {
	store, err := ldcomponents.InMemoryDataStore().CreateDataStore(SDKContextImpl{}, nil)
	if err != nil {
		panic(err)
	}
	return store
}

func UpsertFlag(store interfaces.DataStore, flag ldmodel.FeatureFlag) (bool, error) {
	return store.Upsert(ldstoreimpl.Features(), flag.Key,
		ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag})
}

func UpsertSegment(store interfaces.DataStore, segment ldmodel.Segment) (bool, error) {
	return store.Upsert(ldstoreimpl.Segments(), segment.Key,
		ldstoretypes.ItemDescriptor{Version: segment.Version, Item: &segment})
}
