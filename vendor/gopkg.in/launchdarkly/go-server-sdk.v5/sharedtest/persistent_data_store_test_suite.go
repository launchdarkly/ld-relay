package sharedtest

import (
	"os"
	"testing"

	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	intf "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ShouldSkipDatabaseTests returns true if the environment variable LD_SKIP_DATABASE_TESTS is non-empty.
func ShouldSkipDatabaseTests() bool {
	return os.Getenv("LD_SKIP_DATABASE_TESTS") != ""
}

func assertEqualsSerializedItem(
	t *testing.T,
	item MockDataItem,
	serializedItemDesc intf.StoreSerializedItemDescriptor,
) {
	// This allows for the fact that a PersistentDataStore may not be able to get the item version without
	// deserializing it, so we allow the version to be zero.
	assert.Equal(t, item.ToSerializedItemDescriptor().SerializedItem, serializedItemDesc.SerializedItem)
	if serializedItemDesc.Version != 0 {
		assert.Equal(t, item.Version, serializedItemDesc.Version)
	}
}

func assertEqualsDeletedItem(
	t *testing.T,
	expected intf.StoreSerializedItemDescriptor,
	actual intf.StoreSerializedItemDescriptor,
) {
	// As above, the PersistentDataStore may not have separate access to the version and deleted state;
	// PersistentDataStoreWrapper compensates for this when it deserializes the item.
	if actual.SerializedItem == nil {
		assert.True(t, actual.Deleted)
		assert.Equal(t, expected.Version, actual.Version)
	} else {
		itemDesc, err := MockData.Deserialize(actual.SerializedItem)
		assert.NoError(t, err)
		assert.Equal(t, intf.StoreItemDescriptor{Version: expected.Version}, itemDesc)
	}
}

// PersistentDataStoreTestSuite provides a configurable test suite for all implementations of
// PersistentDataStore.
//
// In order to be testable with this tool, a data store implementation must have the following
// characteristics:
//
// 1. It has some notion of a "prefix" string that can be used to distinguish between different
// SDK instances using the same underlying database.
//
// 2. Two instances of the same data store type with the same configuration, and the same prefix,
// should be able to see each other's data.
type PersistentDataStoreTestSuite struct {
	storeFactoryFn               func(string) intf.PersistentDataStoreFactory
	clearDataFn                  func(string) error
	errorStoreFactory            intf.PersistentDataStoreFactory
	errorValidator               func(*testing.T, error)
	concurrentModificationHookFn func(store intf.PersistentDataStore, hook func())
	alwaysRun                    bool
}

// NewPersistentDataStoreTestSuite creates a PersistentDataStoreTestSuite for testing some
// implementation of PersistentDataStore.
//
// The storeFactoryFn parameter is a function that takes a prefix string and returns a configured
// factory for this data store type (for instance, ldconsul.DataStore().Prefix(prefix)). If the
// prefix string is "", it should use the default prefix defined by the data store implementation.
// The factory must include any necessary configuration that may be appropriate for the test
// environment (for instance, pointing it to a database instance that has been set up for the
// tests).
//
// The clearDataFn parameter is a function that takes a prefix string and deletes any existing
// data that may exist in the database corresponding to that prefix.
func NewPersistentDataStoreTestSuite(
	storeFactoryFn func(prefix string) intf.PersistentDataStoreFactory,
	clearDataFn func(prefix string) error,
) *PersistentDataStoreTestSuite {
	return &PersistentDataStoreTestSuite{
		storeFactoryFn: storeFactoryFn,
		clearDataFn:    clearDataFn,
	}
}

// ErrorStoreFactory enables a test of error handling. The provided errorStoreFactory is expected to
// produce a data store instance whose operations should all fail and return an error. The errorValidator
// function, if any, will be called to verify that it is the expected error.
func (s *PersistentDataStoreTestSuite) ErrorStoreFactory(
	errorStoreFactory intf.PersistentDataStoreFactory,
	errorValidator func(*testing.T, error),
) *PersistentDataStoreTestSuite {
	s.errorStoreFactory = errorStoreFactory
	s.errorValidator = errorValidator
	return s
}

// ConcurrentModificationHook enables tests of concurrent modification behavior, for store
// implementations that support testing this.
//
// The hook parameter is a function which, when called with a store instance and another function as
// parameters, will modify the store instance so that it will call the latter function synchronously
// during each Upsert operation - after the old value has been read, but before the new one has been
// written.
func (s *PersistentDataStoreTestSuite) ConcurrentModificationHook(
	setHookFn func(store intf.PersistentDataStore, hook func()),
) *PersistentDataStoreTestSuite {
	s.concurrentModificationHookFn = setHookFn
	return s
}

// AlwaysRun specifies whether this test suite should always be run even if the environment variable
// LD_SKIP_DATABASE_TESTS is set.
func (s *PersistentDataStoreTestSuite) AlwaysRun(alwaysRun bool) *PersistentDataStoreTestSuite {
	s.alwaysRun = alwaysRun
	return s
}

// Run runs the configured test suite.
func (s *PersistentDataStoreTestSuite) Run(t *testing.T) {
	if !s.alwaysRun && ShouldSkipDatabaseTests() {
		return
	}

	t.Run("Init", s.runInitTests)
	t.Run("Get", s.runGetTests)
	t.Run("Upsert", s.runUpsertTests)
	t.Run("Delete", s.runDeleteTests)

	t.Run("IsStoreAvailable", func(t *testing.T) {
		// The store should always be available during this test suite
		s.withDefaultStore(func(store intf.PersistentDataStore) {
			assert.True(t, store.IsStoreAvailable())
		})
	})

	t.Run("error returns", s.runErrorTests)
	t.Run("prefix independence", s.runPrefixIndependenceTests)
	t.Run("concurrent modification", s.runConcurrentModificationTests)
}

func (s *PersistentDataStoreTestSuite) makeStore(prefix string) intf.PersistentDataStore {
	store, err := s.storeFactoryFn(prefix).CreatePersistentDataStore(NewSimpleTestContext(""))
	if err != nil {
		panic(err)
	}
	return store
}

func (s *PersistentDataStoreTestSuite) clearData(prefix string) {
	err := s.clearDataFn(prefix)
	if err != nil {
		panic(err)
	}
}

func (s *PersistentDataStoreTestSuite) initWithEmptyData(store intf.PersistentDataStore) {
	err := store.Init(MakeSerializedMockDataSet())
	if err != nil {
		panic(err)
	}
}

func (s *PersistentDataStoreTestSuite) withDefaultStore(action func(intf.PersistentDataStore)) {
	store := s.makeStore("")
	defer store.Close() //nolint:errcheck
	action(store)
}

func (s *PersistentDataStoreTestSuite) withDefaultInitedStore(action func(intf.PersistentDataStore)) {
	s.clearData("")
	store := s.makeStore("")
	defer store.Close() //nolint:errcheck
	s.initWithEmptyData(store)
	action(store)
}

func (s *PersistentDataStoreTestSuite) runInitTests(t *testing.T) {
	t.Run("store initialized after init", func(t *testing.T) {
		s.clearData("")
		s.withDefaultStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature"}
			allData := MakeSerializedMockDataSet(item1)
			require.NoError(t, store.Init(allData))

			assert.True(t, store.IsInitialized())
		})
	})

	t.Run("completely replaces previous data", func(t *testing.T) {
		s.clearData("")
		s.withDefaultStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "first", Version: 1}
			item2 := MockDataItem{Key: "second", Version: 1}
			otherItem1 := MockDataItem{Key: "first", Version: 1, IsOtherKind: true}
			allData := MakeSerializedMockDataSet(item1, item2, otherItem1)
			require.NoError(t, store.Init(allData))

			items, err := store.GetAll(MockData)
			require.NoError(t, err)
			assert.Len(t, items, 2)
			assertEqualsSerializedItem(t, item1, itemDescriptorsToMap(items)[item1.Key])
			assertEqualsSerializedItem(t, item2, itemDescriptorsToMap(items)[item2.Key])

			otherItems, err := store.GetAll(MockOtherData)
			require.NoError(t, err)
			assert.Len(t, otherItems, 1)
			assertEqualsSerializedItem(t, otherItem1, itemDescriptorsToMap(otherItems)[otherItem1.Key])

			otherItem2 := MockDataItem{Key: "second", Version: 1, IsOtherKind: true}
			allData = MakeSerializedMockDataSet(item1, otherItem2)
			require.NoError(t, store.Init(allData))

			items, err = store.GetAll(MockData)
			require.NoError(t, err)
			assert.Len(t, items, 1)
			assertEqualsSerializedItem(t, item1, itemDescriptorsToMap(items)[item1.Key])

			otherItems, err = store.GetAll(MockOtherData)
			require.NoError(t, err)
			assert.Len(t, otherItems, 1)
			assertEqualsSerializedItem(t, otherItem2, itemDescriptorsToMap(otherItems)[otherItem2.Key])
		})
	})

	t.Run("one instance can detect if another instance has initialized the store", func(t *testing.T) {
		s.clearData("")
		s.withDefaultStore(func(store1 intf.PersistentDataStore) {
			s.withDefaultStore(func(store2 intf.PersistentDataStore) {
				assert.False(t, store1.IsInitialized())

				s.initWithEmptyData(store2)

				assert.True(t, store1.IsInitialized())
			})
		})
	})
}

func (s *PersistentDataStoreTestSuite) runGetTests(t *testing.T) {
	t.Run("existing item", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature"}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsSerializedItem(t, item1, result)
		})
	})

	t.Run("nonexisting item", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			result, err := store.Get(MockData, "no")
			assert.NoError(t, err)
			assert.Equal(t, -1, result.Version)
			assert.Nil(t, result.SerializedItem)
		})
	})

	t.Run("all items", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			result, err := store.GetAll(MockData)
			assert.NoError(t, err)
			assert.Len(t, result, 0)

			item1 := MockDataItem{Key: "first", Version: 1}
			item2 := MockDataItem{Key: "second", Version: 1}
			otherItem1 := MockDataItem{Key: "first", Version: 1, IsOtherKind: true}
			_, err = store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			_, err = store.Upsert(MockData, item2.Key, item2.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			_, err = store.Upsert(MockOtherData, otherItem1.Key, otherItem1.ToSerializedItemDescriptor())
			assert.NoError(t, err)

			result, err = store.GetAll(MockData)
			assert.NoError(t, err)
			assert.Len(t, result, 2)
			assertEqualsSerializedItem(t, item1, itemDescriptorsToMap(result)[item1.Key])
			assertEqualsSerializedItem(t, item2, itemDescriptorsToMap(result)[item2.Key])
		})
	})
}

func (s *PersistentDataStoreTestSuite) runUpsertTests(t *testing.T) {
	t.Run("newer version", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10, Name: "original"}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			item1a := MockDataItem{Key: "feature", Version: item1.Version + 1, Name: "updated"}
			updated, err = store.Upsert(MockData, item1.Key, item1a.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsSerializedItem(t, item1a, result)
		})
	})

	t.Run("older version", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10, Name: "original"}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			item1a := MockDataItem{Key: "feature", Version: item1.Version - 1, Name: "updated"}
			updated, err = store.Upsert(MockData, item1.Key, item1a.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.False(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsSerializedItem(t, item1, result)
		})
	})

	t.Run("same version", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10, Name: "updated"}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			item1a := MockDataItem{Key: "feature", Version: item1.Version, Name: "updated"}
			updated, err = store.Upsert(MockData, item1.Key, item1a.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.False(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsSerializedItem(t, item1, result)
		})
	})
}

func (s *PersistentDataStoreTestSuite) runDeleteTests(t *testing.T) {
	t.Run("newer version", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			deletedItem := MockDataItem{Key: item1.Key, Version: item1.Version + 1, Deleted: true}
			updated, err = store.Upsert(MockData, item1.Key, deletedItem.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsDeletedItem(t, deletedItem.ToSerializedItemDescriptor(), result)
		})
	})

	t.Run("older version", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			deletedItem := MockDataItem{Key: item1.Key, Version: item1.Version - 1, Deleted: true}
			updated, err = store.Upsert(MockData, item1.Key, deletedItem.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.False(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsSerializedItem(t, item1, result)
		})
	})

	t.Run("same version", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			deletedItem := MockDataItem{Key: item1.Key, Version: item1.Version, Deleted: true}
			updated, err = store.Upsert(MockData, item1.Key, deletedItem.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.False(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsSerializedItem(t, item1, result)
		})
	})

	t.Run("unknown item", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			deletedItem := MockDataItem{Key: "feature", Version: 1, Deleted: true}
			updated, err := store.Upsert(MockData, deletedItem.Key, deletedItem.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			result, err := store.Get(MockData, deletedItem.Key)
			assert.NoError(t, err)
			assertEqualsDeletedItem(t, deletedItem.ToSerializedItemDescriptor(), result)
		})
	})

	t.Run("upsert older version after delete", func(t *testing.T) {
		s.withDefaultInitedStore(func(store intf.PersistentDataStore) {
			item1 := MockDataItem{Key: "feature", Version: 10}
			updated, err := store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			deletedItem := MockDataItem{Key: item1.Key, Version: item1.Version + 1, Deleted: true}
			updated, err = store.Upsert(MockData, item1.Key, deletedItem.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.True(t, updated)

			updated, err = store.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
			assert.NoError(t, err)
			assert.False(t, updated)

			result, err := store.Get(MockData, item1.Key)
			assert.NoError(t, err)
			assertEqualsDeletedItem(t, deletedItem.ToSerializedItemDescriptor(), result)
		})
	})
}

func (s *PersistentDataStoreTestSuite) runPrefixIndependenceTests(t *testing.T) {
	runWithPrefixes := func(
		t *testing.T,
		name string,
		test func(*testing.T, intf.PersistentDataStore, intf.PersistentDataStore),
	) {
		prefix1 := "testprefix1"
		prefix2 := "testprefix2"
		s.clearData(prefix1)
		s.clearData(prefix2)
		store1 := s.makeStore(prefix1)
		defer store1.Close() //nolint:errcheck
		store2 := s.makeStore(prefix2)
		defer store2.Close() //nolint:errcheck
		t.Run(name, func(t *testing.T) {
			test(t, store1, store2)
		})
	}

	runWithPrefixes(t, "Init", func(t *testing.T, store1 intf.PersistentDataStore, store2 intf.PersistentDataStore) {
		assert.False(t, store1.IsInitialized())
		assert.False(t, store2.IsInitialized())

		item1a := MockDataItem{Key: "flag-a", Version: 1}
		item1b := MockDataItem{Key: "flag-b", Version: 1}
		item2a := MockDataItem{Key: "flag-a", Version: 2}
		item2c := MockDataItem{Key: "flag-c", Version: 2}

		data1 := MakeSerializedMockDataSet(item1a, item1b)
		data2 := MakeSerializedMockDataSet(item2a, item2c)

		err := store1.Init(data1)
		require.NoError(t, err)

		assert.True(t, store1.IsInitialized())
		assert.False(t, store2.IsInitialized())

		err = store2.Init(data2)
		require.NoError(t, err)

		assert.True(t, store1.IsInitialized())
		assert.True(t, store2.IsInitialized())

		newItems1, err := store1.GetAll(MockData)
		require.NoError(t, err)
		assert.Len(t, newItems1, 2)
		assertEqualsSerializedItem(t, item1a, itemDescriptorsToMap(newItems1)[item1a.Key])
		assertEqualsSerializedItem(t, item1b, itemDescriptorsToMap(newItems1)[item1b.Key])

		newItem1a, err := store1.Get(MockData, item1a.Key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item1a, newItem1a)

		newItem1b, err := store1.Get(MockData, item1b.Key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item1b, newItem1b)

		newItems2, err := store2.GetAll(MockData)
		require.NoError(t, err)
		assert.Len(t, newItems2, 2)
		assertEqualsSerializedItem(t, item2a, itemDescriptorsToMap(newItems2)[item2a.Key])
		assertEqualsSerializedItem(t, item2c, itemDescriptorsToMap(newItems2)[item2c.Key])

		newItem2a, err := store2.Get(MockData, item2a.Key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item2a, newItem2a)

		newItem2c, err := store2.Get(MockData, item2c.Key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item2c, newItem2c)
	})

	runWithPrefixes(t, "Upsert/Delete", func(t *testing.T, store1 intf.PersistentDataStore,
		store2 intf.PersistentDataStore) {
		assert.False(t, store1.IsInitialized())
		assert.False(t, store2.IsInitialized())

		key := "flag"
		item1 := MockDataItem{Key: key, Version: 1}
		item2 := MockDataItem{Key: key, Version: 2}

		// Insert the one with the higher version first, so we can verify that the version-checking logic
		// is definitely looking in the right namespace
		updated, err := store2.Upsert(MockData, item2.Key, item2.ToSerializedItemDescriptor())
		require.NoError(t, err)
		assert.True(t, updated)
		_, err = store1.Upsert(MockData, item1.Key, item1.ToSerializedItemDescriptor())
		require.NoError(t, err)
		assert.True(t, updated)

		newItem1, err := store1.Get(MockData, key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item1, newItem1)

		newItem2, err := store2.Get(MockData, key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item2, newItem2)

		updated, err = store1.Upsert(MockData, key, item2.ToSerializedItemDescriptor())
		require.NoError(t, err)
		assert.True(t, updated)

		newItem1a, err := store1.Get(MockData, key)
		require.NoError(t, err)
		assertEqualsSerializedItem(t, item2, newItem1a)
	})
}

func (s *PersistentDataStoreTestSuite) runErrorTests(t *testing.T) {
	if s.errorStoreFactory == nil {
		t.Skip("not implemented for this store type")
		return
	}
	errorValidator := s.errorValidator
	if errorValidator == nil {
		errorValidator = func(*testing.T, error) {}
	}

	store, err := s.errorStoreFactory.CreatePersistentDataStore(NewSimpleTestContext(""))
	require.NoError(t, err)
	defer store.Close() //nolint:errcheck

	t.Run("Init", func(t *testing.T) {
		allData := []intf.StoreSerializedCollection{
			{Kind: intf.DataKindFeatures()},
			{Kind: intf.DataKindSegments()},
		}
		err := store.Init(allData)
		require.Error(t, err)
		errorValidator(t, err)
	})

	t.Run("Get", func(t *testing.T) {
		_, err := store.Get(intf.DataKindFeatures(), "key")
		require.Error(t, err)
		errorValidator(t, err)
	})

	t.Run("GetAll", func(t *testing.T) {
		_, err := store.GetAll(intf.DataKindFeatures())
		require.Error(t, err)
		errorValidator(t, err)
	})

	t.Run("Upsert", func(t *testing.T) {
		desc := FlagDescriptor(ldbuilders.NewFlagBuilder("key").Build())
		sdesc := intf.StoreSerializedItemDescriptor{
			Version:        1,
			SerializedItem: intf.DataKindFeatures().Serialize(desc),
		}
		_, err := store.Upsert(intf.DataKindFeatures(), "key", sdesc)
		require.Error(t, err)
		errorValidator(t, err)
	})

	t.Run("IsInitialized", func(t *testing.T) {
		assert.False(t, store.IsInitialized())
	})
}

func (s *PersistentDataStoreTestSuite) runConcurrentModificationTests(t *testing.T) {
	if s.concurrentModificationHookFn == nil {
		t.Skip("not implemented for this store type")
		return
	}

	s.clearData("")
	store1 := s.makeStore("")
	defer store1.Close() //nolint:errcheck
	store2 := s.makeStore("")
	defer store2.Close() //nolint:errcheck

	key := "foo"

	makeItemWithVersion := func(version int) MockDataItem {
		return MockDataItem{Key: key, Version: version}
	}

	setupStore1 := func(initialVersion int) {
		allData := MakeSerializedMockDataSet(makeItemWithVersion(initialVersion))
		require.NoError(t, store1.Init(allData))
	}

	setupConcurrentModifierToWriteVersions := func(versionsToWrite ...int) {
		i := 0
		s.concurrentModificationHookFn(store1, func() {
			if i < len(versionsToWrite) {
				newItem := makeItemWithVersion(versionsToWrite[i])
				_, err := store2.Upsert(MockData, key, newItem.ToSerializedItemDescriptor())
				require.NoError(t, err)
				i++
			}
		})
	}

	t.Run("upsert race condition against external client with lower version", func(t *testing.T) {
		setupStore1(1)
		setupConcurrentModifierToWriteVersions(2, 3, 4)

		_, err := store1.Upsert(MockData, key, makeItemWithVersion(10).ToSerializedItemDescriptor())
		assert.NoError(t, err)

		var result intf.StoreSerializedItemDescriptor
		result, err = store1.Get(MockData, key)
		assert.NoError(t, err)
		assertEqualsSerializedItem(t, makeItemWithVersion(10), result)
	})

	t.Run("upsert race condition against external client with higher version", func(t *testing.T) {
		setupStore1(1)
		setupConcurrentModifierToWriteVersions(3)

		updated, err := store1.Upsert(MockData, key, makeItemWithVersion(2).ToSerializedItemDescriptor())
		assert.NoError(t, err)
		assert.False(t, updated)

		var result intf.StoreSerializedItemDescriptor
		result, err = store1.Get(MockData, key)
		assert.NoError(t, err)
		assertEqualsSerializedItem(t, makeItemWithVersion(3), result)
	})
}
