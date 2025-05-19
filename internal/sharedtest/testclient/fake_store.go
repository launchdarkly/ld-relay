package testclient

import (
	"encoding/json"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

type FakeStore struct {
	collections []ldstoretypes.Collection
}

func NewFakeStore(collections []ldstoretypes.Collection) *FakeStore {
	return &FakeStore{collections: collections}
}

func (s *FakeStore) Close() error {
	return nil
}

func (s *FakeStore) Selector() subsystems.Selector {
	return subsystems.NoSelector()
}

func (s *FakeStore) SetBasis(changes []subsystems.Change, selector subsystems.Selector, persist bool) {
	s.collections = make([]ldstoretypes.Collection, 2)
	s.collections[0] = ldstoretypes.Collection{
		Kind:  ldstoreimpl.Features(),
		Items: make([]ldstoretypes.KeyedItemDescriptor, 0),
	}
	s.collections[1] = ldstoretypes.Collection{
		Kind:  ldstoreimpl.Segments(),
		Items: make([]ldstoretypes.KeyedItemDescriptor, 0),
	}

	for _, change := range changes {
		if kind := change.Kind; kind == subsystems.FlagKind {
			s.collections[0].Items = append(s.collections[0].Items, ldstoretypes.KeyedItemDescriptor{
				Key: change.Key,
				Item: ldstoretypes.ItemDescriptor{
					Version: change.Version,
					Item:    change.Object,
				},
			})
		} else if kind == subsystems.SegmentKind {
			s.collections[1].Items = append(s.collections[1].Items, ldstoretypes.KeyedItemDescriptor{
				Key: change.Key,
				Item: ldstoretypes.ItemDescriptor{
					Version: change.Version,
					Item:    change.Object,
				},
			})
		}
	}
}

func (s *FakeStore) ApplyDelta(changes []subsystems.Change, selector subsystems.Selector, persist bool) {
	for _, change := range changes {
		if kind := change.Kind; kind == subsystems.FlagKind {
			// Add to Flags collection
			for i, collection := range s.collections {
				if collection.Kind.GetName() == "features" {
					var flag ldmodel.FeatureFlag
					if err := json.Unmarshal(change.Object, &flag); err != nil {
						panic(err)
					}
					s.collections[i].Items = append(s.collections[i].Items, ldstoretypes.KeyedItemDescriptor{
						Key: change.Key,
						Item: ldstoretypes.ItemDescriptor{
							Version: change.Version,
							Item:    &flag,
						},
					})
					break
				}
			}
		} else if kind == subsystems.SegmentKind {
			// Add to Segments collection
			for i, collection := range s.collections {
				if collection.Kind.GetName() == "segments" {
					var segment ldmodel.Segment
					if err := json.Unmarshal(change.Object, &segment); err != nil {
						panic(err)
					}
					s.collections[i].Items = append(s.collections[i].Items, ldstoretypes.KeyedItemDescriptor{
						Key: change.Key,
						Item: ldstoretypes.ItemDescriptor{
							Version: change.Version,
							Item:    &segment,
						},
					})
					break
				}
			}
		}
	}
}

func (s *FakeStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	for _, collection := range s.collections {
		if collection.Kind == kind {
			for _, item := range collection.Items {
				if item.Key == key {
					return item.Item, nil
				}
			}
		}
	}

	return ldstoretypes.ItemDescriptor{}, nil
}

func (s *FakeStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	result := []ldstoretypes.KeyedItemDescriptor{}
	for _, collection := range s.collections {
		if collection.Kind == kind {
			result = append(result, collection.Items...)
		}
	}
	return result, nil
}

func (s *FakeStore) IsInitialized() bool {
	return true
}

func (s *FakeStore) Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
	result := make(map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor)
	for _, collection := range s.collections {
		result[collection.Kind] = collection.Items
	}

	return result, subsystems.NoSelector(), nil
}
