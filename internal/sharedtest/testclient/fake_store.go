package testclient

import (
	"sync"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

type FakeStore struct {
	collections []ldstoretypes.Collection
	selector    subsystems.Selector
	mu          sync.Mutex
}

func NewFakeStore(collections []ldstoretypes.Collection) *FakeStore {
	return &FakeStore{
		collections: collections,
		selector:    subsystems.NewSelector("initial-state", 1),
	}
}

func (s *FakeStore) Close() error {
	return nil
}

func (s *FakeStore) Selector() subsystems.Selector {
	s.mu.Lock()
	selector := s.selector
	s.mu.Unlock()

	return selector
}

func (s *FakeStore) Apply(changeSet subsystems.ChangeSet) {
	switch changeSet.IntentCode() {
	case subsystems.IntentTransferFull:
		s.setBasis(changeSet.Changes(), changeSet.Selector())
	case subsystems.IntentTransferChanges:
		s.applyDelta(changeSet.Changes(), changeSet.Selector())
	}
}

func (s *FakeStore) setBasis(changes []subsystems.Change, selector subsystems.Selector) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
			var flag ldmodel.FeatureFlag
			if err := flag.UnmarshalJSON(change.Object); err != nil {
				panic(err)
			}
			s.collections[0].Items = append(s.collections[0].Items, ldstoretypes.KeyedItemDescriptor{
				Key: change.Key,
				Item: ldstoretypes.ItemDescriptor{
					Version: change.Version,
					Item:    &flag,
				},
			})
		} else if kind == subsystems.SegmentKind {
			var segment ldmodel.Segment
			if err := segment.UnmarshalJSON(change.Object); err != nil {
				panic(err)
			}
			s.collections[1].Items = append(s.collections[1].Items, ldstoretypes.KeyedItemDescriptor{
				Key: change.Key,
				Item: ldstoretypes.ItemDescriptor{
					Version: change.Version,
					Item:    &segment,
				},
			})
		}
	}
	s.selector = selector
}

func (s *FakeStore) applyDelta(changes []subsystems.Change, selector subsystems.Selector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, change := range changes {
		if kind := change.Kind; kind == subsystems.FlagKind {
			// Add to Flags collection
			for i, collection := range s.collections {
				if collection.Kind.GetName() == "features" {
					var flag ldmodel.FeatureFlag
					if err := flag.UnmarshalJSON(change.Object); err != nil {
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
					if err := segment.UnmarshalJSON(change.Object); err != nil {
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
	s.selector = selector
}

func (s *FakeStore) InvalidateClientSideState() {
	// This method is a no-op in the fake store.
}

func (s *FakeStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []ldstoretypes.KeyedItemDescriptor{}
	for _, collection := range s.collections {
		if collection.Kind == kind {
			result = append(result, collection.Items...)
		}
	}
	return result, nil
}

func (s *FakeStore) IsInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selector.IsDefined()
}

func (s *FakeStore) Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor)
	for _, collection := range s.collections {
		result[collection.Kind] = collection.Items
	}

	return result, s.selector, nil
}
