package sdks

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// replicaStore is a persistent store in memory that acts like a read replica. Like the Redis store, it
// does not set the version in the descriptors that it returns.
type replicaStore struct {
	mu      sync.Mutex
	items   map[string]map[string][]byte
	inited  bool
	writes  int
	readErr error
	closed  bool
	// getStarted, if not nil, is closed when the first Get starts. That Get then waits for getRelease.
	getStarted chan struct{}
	getRelease chan struct{}
	getOnce    sync.Once
	// availabilityRelease, if not nil, holds each availability check until it is closed. The first
	// availability check closes availabilityStarted.
	availabilityRelease chan struct{}
	availabilityStarted chan struct{}
	availabilityOnce    sync.Once
}

func newReplicaStore() *replicaStore {
	return &replicaStore{items: map[string]map[string][]byte{}}
}

func (f *replicaStore) put(kind ldstoretypes.DataKind, key string, version int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items[kind.GetName()] == nil {
		f.items[kind.GetName()] = map[string][]byte{}
	}
	f.items[kind.GetName()][key] = serializedFlag(key, version).SerializedItem
	f.inited = true
}

func (f *replicaStore) setReadErr(err error) {
	f.mu.Lock()
	f.readErr = err
	f.mu.Unlock()
}

func (f *replicaStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *replicaStore) Init([]ldstoretypes.SerializedCollection) error {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil
}

func (f *replicaStore) Upsert(ldstoretypes.DataKind, string, ldstoretypes.SerializedItemDescriptor) (bool, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return true, nil
}

func (f *replicaStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.SerializedItemDescriptor, error) {
	if f.availabilityRelease != nil && key == readOnlyAvailabilityKey {
		f.availabilityOnce.Do(func() { close(f.availabilityStarted) })
		<-f.availabilityRelease
	}
	if f.getStarted != nil && key != readOnlyAvailabilityKey {
		f.getOnce.Do(func() {
			close(f.getStarted)
			<-f.getRelease
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return ldstoretypes.SerializedItemDescriptor{}, f.readErr
	}
	if b, ok := f.items[kind.GetName()][key]; ok {
		return ldstoretypes.SerializedItemDescriptor{SerializedItem: b}, nil
	}
	return ldstoretypes.SerializedItemDescriptor{}.NotFound(), nil
}

func (f *replicaStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedSerializedItemDescriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	var result []ldstoretypes.KeyedSerializedItemDescriptor
	for k, b := range f.items[kind.GetName()] {
		result = append(result, ldstoretypes.KeyedSerializedItemDescriptor{
			Key: k, Item: ldstoretypes.SerializedItemDescriptor{SerializedItem: b},
		})
	}
	return result, nil
}

func (f *replicaStore) IsInitialized() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inited
}

// IsStoreAvailable writes, like the Redis store in atomic mode.
func (f *replicaStore) IsStoreAvailable() bool {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return true
}

func (f *replicaStore) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

type replicaStoreConfigurer struct{ store *replicaStore }

func (c replicaStoreConfigurer) Build(subsystems.ClientContext) (subsystems.PersistentDataStore, error) {
	return c.store, nil
}

type noOpStatusSink struct{}

func (noOpStatusSink) UpdateStatus(interfaces.DataStoreStatus) {}

func serializedFlag(key string, version int) ldstoretypes.SerializedItemDescriptor {
	item := flagItem(key, version)
	return ldstoretypes.SerializedItemDescriptor{
		Version:        version,
		SerializedItem: ldstoreimpl.Features().Serialize(item),
	}
}

func flagItem(key string, version int) ldstoretypes.ItemDescriptor {
	flag := ldbuilders.NewFlagBuilder(key).Version(version).Build()
	return ldstoretypes.ItemDescriptor{Version: version, Item: &flag}
}

func flagData(flags map[string]int) []ldstoretypes.Collection {
	var items []ldstoretypes.KeyedItemDescriptor
	for key, version := range flags {
		items = append(items, ldstoretypes.KeyedItemDescriptor{Key: key, Item: flagItem(key, version)})
	}
	return []ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: items}, {Kind: ldstoreimpl.Segments()}}
}

// makeReadOnlyStore gives the data store that Relay uses in read-only mode, over the replica.
func makeReadOnlyStore(t *testing.T, inner *replicaStore) subsystems.DataStore {
	return makeReadOnlyStoreWithSink(t, inner, noOpStatusSink{})
}

func makeReadOnlyStoreWithSink(
	t *testing.T,
	inner *replicaStore,
	sink subsystems.DataStoreUpdateSink,
) subsystems.DataStore {
	ctx := subsystems.BasicClientContext{
		DataStoreUpdateSink: sink,
		Logging:             subsystems.LoggingConfiguration{Loggers: ldlog.NewDisabledLoggers()},
	}
	configurer := replicaStoreConfigurer{inner}
	store, err := newReadOnlyDataStore(configurer, func() subsystems.ComponentConfigurer[subsystems.PersistentDataStore] {
		return configurer
	}).Build(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// liveVersions gives the version of each item that is not deleted.
func liveVersions(t *testing.T, store subsystems.DataStore) map[string]int {
	t.Helper()
	all, err := store.GetAll(ldstoreimpl.Features())
	require.NoError(t, err)
	result := map[string]int{}
	for _, keyed := range all {
		if keyed.Item.Item != nil {
			result[keyed.Key] = keyed.Item.Version
		}
	}
	return result
}

func TestReadOnlyStoreNeverWrites(t *testing.T) {
	inner := newReplicaStore()
	store := makeReadOnlyStore(t, inner)

	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))
	updated, err := store.Upsert(ldstoreimpl.Features(), "a", flagItem("a", 2))
	require.NoError(t, err)
	assert.True(t, updated)
	core := newReadOnlyPersistentStore(inner)
	assert.True(t, core.IsStoreAvailable())

	assert.Equal(t, 0, inner.writeCount())
}

func TestReadOnlyStoreServesStoreBeforeDataFromLaunchDarkly(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 3)
	store := makeReadOnlyStore(t, inner)

	assert.True(t, store.IsInitialized())
	assert.Equal(t, map[string]int{"a": 3}, liveVersions(t, store))
	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 3, item.Version)
}

func TestReadOnlyStoreIsNotInitializedWhenStoreIsEmpty(t *testing.T) {
	store := makeReadOnlyStore(t, newReplicaStore())
	assert.False(t, store.IsInitialized())

	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))
	assert.True(t, store.IsInitialized())
}

func TestReadOnlyStoreReturnsReadErrorsBeforeDataFromLaunchDarkly(t *testing.T) {
	readErr := errors.New("sorry")
	inner := newReplicaStore()
	inner.setReadErr(readErr)
	core := newReadOnlyPersistentStore(inner)

	_, err := core.GetAll(ldstoreimpl.Features())
	assert.Equal(t, readErr, err)
	_, err = core.Get(ldstoreimpl.Features(), "a")
	assert.Equal(t, readErr, err)
	assert.False(t, core.IsStoreAvailable())
}

func TestReadOnlyStoreCloseClosesStore(t *testing.T) {
	inner := newReplicaStore()
	store := makeReadOnlyStore(t, inner)
	require.NoError(t, store.Close())
	assert.True(t, inner.closed)
}

// LaunchDarkly deletes "d" and then makes the data set. The other Relay instances have not stored the
// deletion yet, so the store still has "d".
func TestReadOnlyStoreDoesNotServeItemDeletedWhileConnecting(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	inner.put(ldstoreimpl.Features(), "d", 1)
	store := makeReadOnlyStore(t, inner)

	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))

	item, err := store.Get(ldstoreimpl.Features(), "d")
	require.NoError(t, err)
	assert.Nil(t, item.Item)
	assert.Equal(t, map[string]int{"a": 1}, liveVersions(t, store))
}

// The store does not have an update yet. A read of all flags must still give the update.
func TestReadOnlyStoreListOfAllItemsHasUpdateThatStoreDoesNotHave(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	store := makeReadOnlyStore(t, inner)
	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))

	_, err := store.Upsert(ldstoreimpl.Features(), "a", flagItem("a", 2))
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"a": 2}, liveVersions(t, store))
}

// LaunchDarkly makes the data set before flag "n" exists. Another Relay instance stores "n" before
// this instance gets the data set. Then this instance gets the update that creates "n".
func TestReadOnlyStoreServesItemCreatedWhileConnecting(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	store := makeReadOnlyStore(t, inner)

	inner.put(ldstoreimpl.Features(), "n", 1)
	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))
	_, err := store.Upsert(ldstoreimpl.Features(), "n", flagItem("n", 1))
	require.NoError(t, err)

	item, err := store.Get(ldstoreimpl.Features(), "n")
	require.NoError(t, err)
	assert.NotNil(t, item.Item)
	assert.Equal(t, map[string]int{"a": 1, "n": 1}, liveVersions(t, store))
}

// A data set from LaunchDarkly that has no segments must not cause a read of the segments in the store.
func TestReadOnlyStoreDoesNotReadKindMissingFromDataFromLaunchDarkly(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Segments(), "s", 1)
	store := makeReadOnlyStore(t, inner)

	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})[:1]))

	segments, err := store.GetAll(ldstoreimpl.Segments())
	require.NoError(t, err)
	assert.Empty(t, segments)
}

func TestReadOnlyStoreIgnoresOlderUpdates(t *testing.T) {
	store := makeReadOnlyStore(t, newReplicaStore())
	require.NoError(t, store.Init(flagData(map[string]int{"a": 5})))

	for _, version := range []int{4, 5} {
		updated, err := store.Upsert(ldstoreimpl.Features(), "a", flagItem("a", version))
		require.NoError(t, err)
		assert.False(t, updated, "version %d", version)
	}
	assert.Equal(t, map[string]int{"a": 5}, liveVersions(t, store))

	// A deletion that is older than the item does not delete it.
	_, err := store.Upsert(ldstoreimpl.Features(), "a", ldstoretypes.ItemDescriptor{Version: 3})
	require.NoError(t, err)
	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 5, item.Version)
	assert.NotNil(t, item.Item)

	updated, err := store.Upsert(ldstoreimpl.Features(), "a", ldstoretypes.ItemDescriptor{Version: 6})
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Empty(t, liveVersions(t, store))
}

// A read of the database that is in progress when the data from LaunchDarkly arrives gives the database
// data to its caller. It does not change the data that later reads get, and Init does not wait for it.
func TestReadOnlyStoreReadInProgressDuringInitDoesNotReplaceData(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	inner.getStarted = make(chan struct{})
	inner.getRelease = make(chan struct{})
	statuses := make(chan interfaces.DataStoreStatus, 10)
	store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))

	type result struct {
		item ldstoretypes.ItemDescriptor
		err  error
	}
	results := make(chan result, 1)
	go func() {
		item, err := store.Get(ldstoreimpl.Features(), "a")
		results <- result{item, err}
	}()
	waitFor(t, inner.getStarted)

	initDone := make(chan error, 1)
	go func() { initDone <- store.Init(flagData(map[string]int{"a": 5})) }()
	select {
	case err := <-initDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Init waited for a store read")
	}
	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 5, item.Version)

	close(inner.getRelease)
	r := <-results
	require.NoError(t, r.err)
	assert.Equal(t, 1, r.item.Version)

	item, err = store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 5, item.Version)
	assert.Equal(t, map[string]int{"a": 5}, liveVersions(t, store))
	assertNoUnavailableStatus(t, statuses)
}

// A read of the database that fails after the data from LaunchDarkly arrives does not change the store
// status. Relay does not use the database again.
func TestReadOnlyStoreErrorAfterDataFromLaunchDarklyDoesNotChangeStatus(t *testing.T) {
	inner := newReplicaStore()
	inner.getStarted = make(chan struct{})
	inner.getRelease = make(chan struct{})
	statuses := make(chan interfaces.DataStoreStatus, 10)
	store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))

	errs := make(chan error, 1)
	go func() {
		_, err := store.Get(ldstoreimpl.Features(), "a")
		errs <- err
	}()
	waitFor(t, inner.getStarted)
	require.NoError(t, store.Init(flagData(map[string]int{"a": 5})))
	inner.setReadErr(errors.New("sorry"))
	close(inner.getRelease)
	require.Error(t, <-errs)

	assertNoUnavailableStatus(t, statuses)
}

// The database fails before the data from LaunchDarkly arrives. The status becomes available when the
// data arrives, though the database still fails.
func TestReadOnlyStoreStatusRecoversWhenDataFromLaunchDarklyArrives(t *testing.T) {
	for _, hang := range []bool{false, true} {
		t.Run(fmt.Sprintf("availability check hangs=%t", hang), func(t *testing.T) {
			inner := newReplicaStore()
			if hang {
				inner.availabilityRelease = make(chan struct{})
				inner.availabilityStarted = make(chan struct{})
				defer close(inner.availabilityRelease)
			}
			statuses := make(chan interfaces.DataStoreStatus, 10)
			store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))

			inner.setReadErr(errors.New("sorry"))
			_, err := store.Get(ldstoreimpl.Features(), "a")
			require.Error(t, err)
			require.False(t, (<-statuses).Available)
			if hang {
				waitFor(t, inner.availabilityStarted)
			}

			require.NoError(t, store.Init(flagData(map[string]int{"a": 5})))
			select {
			case status := <-statuses:
				assert.True(t, status.Available)
				assert.False(t, status.NeedsRefresh)
			case <-time.After(2 * time.Second):
				assert.Fail(t, "the status did not become available")
			}
		})
	}
}

// The SDK sends a store status with a blocking send to a buffer of 10 items. The streaming data source
// reads that buffer on the goroutine that calls Init. Before the data source connects, a database that
// fails and recovers can fill the buffer. Then a read blocks in the status update. Init must not wait
// for that read.
func TestReadOnlyStoreReadBlockedInStatusUpdateDoesNotBlockInit(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	statuses := make(chan interfaces.DataStoreStatus, 10)
	store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))
	readErr := errors.New("max number of clients reached")

	for i := 0; i < 5; i++ {
		inner.setReadErr(readErr)
		_, err := store.Get(ldstoreimpl.Features(), "b")
		require.Error(t, err)
		inner.setReadErr(nil)
		require.Eventually(t, func() bool { return len(statuses) == 2*(i+1) }, 3*time.Second, 10*time.Millisecond)
	}

	inner.setReadErr(readErr)
	readDone := make(chan struct{})
	go func() {
		_, _ = store.Get(ldstoreimpl.Features(), "c")
		close(readDone)
	}()
	time.Sleep(100 * time.Millisecond)

	initDone := make(chan error, 1)
	go func() { initDone <- store.Init(flagData(map[string]int{"a": 2})) }()
	select {
	case err := <-initDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "Init waited for a read that is blocked in a status update")
	}
	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 2, item.Version)

	// The data source reads the buffer again.
	go func() {
		for range statuses {
		}
	}()
	<-readDone
}

// Reads during the first Init get the database data or the data from LaunchDarkly. They never get
// NotFound for a flag that both have.
func TestReadOnlyStoreReadsDuringFirstInitFindExistingFlags(t *testing.T) {
	const keys = 500
	for attempt := 0; attempt < 20; attempt++ {
		inner := newReplicaStore()
		ld := map[string]int{}
		for i := 0; i < keys; i++ {
			k := fmt.Sprintf("k%d", i)
			inner.put(ldstoreimpl.Features(), k, 1)
			ld[k] = 2
		}
		store := makeReadOnlyStore(t, inner)
		data := flagData(ld)
		var next, wrong int64 = -1, 0
		var wg sync.WaitGroup
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					i := atomic.AddInt64(&next, 1)
					if i >= keys {
						return
					}
					item, err := store.Get(ldstoreimpl.Features(), fmt.Sprintf("k%d", i))
					if err != nil || item.Item == nil {
						atomic.AddInt64(&wrong, 1)
					}
				}
			}()
		}
		require.NoError(t, store.Init(data))
		wg.Wait()
		require.Zero(t, wrong, "attempt %d", attempt)
	}
}

// slowInitStore is a memory store whose Init waits until release is closed.
type slowInitStore struct {
	subsystems.DataStore
	started chan struct{}
	release chan struct{}
}

func (s slowInitStore) Init(allData []ldstoretypes.Collection) error {
	close(s.started)
	<-s.release
	return s.DataStore.Init(allData)
}

// Reads use the database store until the memory store has the data from LaunchDarkly.
func TestReadOnlyStoreReadsUseDatabaseUntilMemoryStoreHasData(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	built := makeReadOnlyStore(t, inner).(*readOnlyDataStore)
	memory := slowInitStore{DataStore: built.memory, started: make(chan struct{}), release: make(chan struct{})}
	store := &readOnlyDataStore{database: built.database, memory: memory, core: built.core}

	initDone := make(chan error, 1)
	go func() { initDone <- store.Init(flagData(map[string]int{"a": 2})) }()
	waitFor(t, memory.started)
	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 1, item.Version)
	assert.True(t, store.IsInitialized())

	close(memory.release)
	require.NoError(t, <-initDone)
	item, err = store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 2, item.Version)
}

// Two SDK clients can update the store at the same time, while Relay changes the connection to
// LaunchDarkly. The newest version wins.
func TestReadOnlyStoreConcurrentUpsertsKeepNewestVersion(t *testing.T) {
	for i := 0; i < 1000; i++ {
		store := makeReadOnlyStore(t, newReplicaStore())
		require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))
		var start, wg sync.WaitGroup
		start.Add(1)
		for _, v := range []int{5, 6} {
			wg.Add(1)
			go func(v int) {
				defer wg.Done()
				start.Wait()
				_, _ = store.Upsert(ldstoreimpl.Features(), "a", flagItem("a", v))
			}(v)
		}
		start.Done()
		wg.Wait()
		item, err := store.Get(ldstoreimpl.Features(), "a")
		require.NoError(t, err)
		require.Equal(t, 6, item.Version, "attempt %d", i)
		require.Equal(t, map[string]int{"a": 6}, liveVersions(t, store), "attempt %d", i)
	}
}

// An update concurrent with a new data set gives the same result as some order of the two.
func TestReadOnlyStoreUpsertConcurrentWithInit(t *testing.T) {
	newData := map[string]int{}
	for i := 0; i < 200; i++ {
		newData[fmt.Sprintf("f%d", i)] = 2
	}
	for i := 0; i < 500; i++ {
		store := makeReadOnlyStore(t, newReplicaStore())
		require.NoError(t, store.Init(flagData(map[string]int{"f0": 1, "old": 1})))
		var start, wg sync.WaitGroup
		start.Add(1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			start.Wait()
			_ = store.Init(flagData(newData))
		}()
		go func() {
			defer wg.Done()
			start.Wait()
			_, _ = store.Upsert(ldstoreimpl.Features(), "f1", flagItem("f1", 3))
		}()
		start.Done()
		wg.Wait()
		live := liveVersions(t, store)
		require.Contains(t, []int{2, 3}, live["f1"], "attempt %d", i)
		delete(live, "f1")
		require.Len(t, live, len(newData)-1, "attempt %d", i)
		require.NotContains(t, live, "old", "attempt %d", i)
	}
}

// Versions are compared with deleted items too.
func TestReadOnlyStoreUpsertVersionCheckWithDeletedItems(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "r", 9)
	store := makeReadOnlyStore(t, inner)
	require.NoError(t, store.Init(flagData(map[string]int{"a": 1})))

	updated, err := store.Upsert(ldstoreimpl.Features(), "a", ldstoretypes.ItemDescriptor{Version: 3})
	require.NoError(t, err)
	assert.True(t, updated)
	for _, version := range []int{2, 3} {
		updated, err = store.Upsert(ldstoreimpl.Features(), "a", flagItem("a", version))
		require.NoError(t, err)
		assert.False(t, updated, "version %d", version)
	}
	assert.Empty(t, liveVersions(t, store))

	// The version of an item that is only in the database does not matter after Init.
	updated, err = store.Upsert(ldstoreimpl.Features(), "r", flagItem("r", 2))
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, map[string]int{"r": 2}, liveVersions(t, store))
}

// A database read that fails after Close starts an SDK status poller. The poller stops at its next
// check, though the database still fails.
func TestReadOnlyStoreStatusPollerStopsAfterClose(t *testing.T) {
	inner := newReplicaStore()
	inner.setReadErr(errors.New("closed pool"))
	statuses := make(chan interfaces.DataStoreStatus, 10)
	store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))
	require.NoError(t, store.Close())

	_, err := store.Get(ldstoreimpl.Features(), "a")
	require.Error(t, err)
	require.False(t, (<-statuses).Available)
	select {
	case status := <-statuses:
		assert.True(t, status.Available)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "the status poller did not stop")
	}
}

// A status poller that waits for a database that does not respond stops when the store closes.
func TestReadOnlyStoreClosedStoreStopsStatusPollerThatWaitsForDatabase(t *testing.T) {
	inner := newReplicaStore()
	inner.availabilityRelease = make(chan struct{})
	inner.availabilityStarted = make(chan struct{})
	defer close(inner.availabilityRelease)
	inner.setReadErr(errors.New("sorry"))
	statuses := make(chan interfaces.DataStoreStatus, 10)
	store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))

	_, err := store.Get(ldstoreimpl.Features(), "a")
	require.Error(t, err)
	require.False(t, (<-statuses).Available)
	waitFor(t, inner.availabilityStarted)
	require.NoError(t, store.Close())
	select {
	case status := <-statuses:
		assert.True(t, status.Available)
	case <-time.After(2 * time.Second):
		assert.Fail(t, "the status poller did not stop")
	}
}

// Init releases the cache of the database store. The SDK wrapper must have the method that does this.
func TestReadOnlyStoreDatabaseStoreCanDropCache(t *testing.T) {
	store := makeReadOnlyStore(t, newReplicaStore()).(*readOnlyDataStore)
	_, ok := store.database.(interface{ DropCache() })
	assert.True(t, ok)
}

// waitFor waits until ch is closed, so that a test fails instead of hanging.
func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out")
	}
}

func assertNoUnavailableStatus(t *testing.T, statuses <-chan interfaces.DataStoreStatus) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case status := <-statuses:
			assert.True(t, status.Available, "the store status became unavailable")
		case <-deadline:
			return
		}
	}
}

// A reconnect gives a new data set while SDKs read all flags. No read can put the store's data into the
// SDK cache.
func TestReadOnlyStoreReadsDuringReconnectDoNotReplaceData(t *testing.T) {
	for i := 0; i < 50; i++ {
		inner := newReplicaStore()
		inner.put(ldstoreimpl.Features(), "a", 1)
		inner.put(ldstoreimpl.Features(), "d", 1)
		store := makeReadOnlyStore(t, inner)
		require.NoError(t, store.Init(flagData(map[string]int{"a": 1, "d": 1})))

		stop := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_, _ = store.GetAll(ldstoreimpl.Features())
						_, _ = store.Get(ldstoreimpl.Features(), "a")
					}
				}
			}()
		}
		// LaunchDarkly deleted "d" and updated "a" while this instance was disconnected.
		require.NoError(t, store.Init(flagData(map[string]int{"a": 2})))
		close(stop)
		wg.Wait()

		require.Equal(t, map[string]int{"a": 2}, liveVersions(t, store), "attempt %d", i)
		item, err := store.Get(ldstoreimpl.Features(), "a")
		require.NoError(t, err)
		require.Equal(t, 2, item.Version, "attempt %d", i)
	}
}

// After a store outage, the SDK calls the Init of the persistent store to restore the store. That call
// does not mean that the data from LaunchDarkly arrived, so the store data is still served.
func TestReadOnlyStoreOutageBeforeDataFromLaunchDarklyDoesNotStopReads(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	statuses := make(chan interfaces.DataStoreStatus, 10)
	store := makeReadOnlyStoreWithSink(t, inner, statusSink(statuses))

	inner.setReadErr(errors.New("sorry"))
	_, err := store.Get(ldstoreimpl.Features(), "a")
	require.Error(t, err)
	require.False(t, (<-statuses).Available)
	inner.setReadErr(nil)
	select {
	case status := <-statuses:
		require.True(t, status.Available)
	case <-time.After(2 * time.Second):
		require.Fail(t, "the store did not recover")
	}

	item, err := store.Get(ldstoreimpl.Features(), "a")
	require.NoError(t, err)
	assert.Equal(t, 1, item.Version)
	assert.Equal(t, 0, inner.writeCount(), "the availability check and the restore do not write")
}

type statusSink chan interfaces.DataStoreStatus

func (s statusSink) UpdateStatus(status interfaces.DataStoreStatus) { s <- status }

func TestReadOnlyStoreConcurrentUse(t *testing.T) {
	inner := newReplicaStore()
	inner.put(ldstoreimpl.Features(), "a", 1)
	store := makeReadOnlyStore(t, inner)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_, _ = store.Get(ldstoreimpl.Features(), "a")
				_, _ = store.GetAll(ldstoreimpl.Features())
				_ = store.IsInitialized()
			}
		}()
	}
	for i := 0; i < 50; i++ {
		require.NoError(t, store.Init(flagData(map[string]int{"a": i + 2})))
		_, err := store.Upsert(ldstoreimpl.Features(), "a", flagItem("a", i+100))
		require.NoError(t, err)
	}
	wg.Wait()
}
