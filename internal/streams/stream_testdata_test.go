package streams

import (
	"errors"
	"sync"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

const (
	testSDKKey    = config.SDKKey("sdk-key")
	testMobileKey = config.MobileKey("mobile-key")
	testEnvID     = config.EnvironmentID("env-id")
)

var (
	fakeError           = errors.New("sorry")
	testFlag1           = ldbuilders.NewFlagBuilder("flag1").Version(1).On(true).Build()
	testFlag2           = ldbuilders.NewFlagBuilder("flag2").Version(1).On(false).Build()
	testSegment1        = ldbuilders.NewSegmentBuilder("segment1").Version(1).Build()
	testFlag1JSON, _    = ldmodel.NewJSONDataModelSerialization().MarshalFeatureFlag(testFlag1)
	testFlag2JSON, _    = ldmodel.NewJSONDataModelSerialization().MarshalFeatureFlag(testFlag2)
	testSegment1JSON, _ = ldmodel.NewJSONDataModelSerialization().MarshalSegment(testSegment1)
	fdv2AllData         = []subsystems.Change{
		{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: 1, Object: testFlag1JSON},
		{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: 1, Object: testSegment1JSON},
	}
	allData = []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testFlag1.Key, Item: sharedtest.FlagDesc(testFlag1)},
			},
		},
		{
			Kind: ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testSegment1.Key, Item: sharedtest.SegmentDesc(testSegment1)},
			},
		},
	}
)

type mockStoreQueries struct {
	isInitializedFn func() bool
	snapshotFn      func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error)

	lock sync.Mutex
}

func newMockStoreQueries() *mockStoreQueries {
	q := &mockStoreQueries{}
	q.setupIsInitialized(true)
	return q
}

func (q *mockStoreQueries) setupIsInitialized(value bool) {
	q.setupIsInitializedFn(func() bool { return value })
}

func (q *mockStoreQueries) setupIsInitializedFn(fn func() bool) {
	q.lock.Lock()
	q.isInitializedFn = fn
	q.lock.Unlock()
}

func (q *mockStoreQueries) setupSnapshotFn(fn func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error)) {
	q.lock.Lock()
	q.snapshotFn = fn
	q.lock.Unlock()
}

func (q *mockStoreQueries) IsInitialized() bool {
	q.lock.Lock()
	fn := q.isInitializedFn
	q.lock.Unlock()
	if fn != nil {
		return fn()
	}
	return false
}

func (q *mockStoreQueries) Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
	q.lock.Lock()
	fn := q.snapshotFn
	q.lock.Unlock()

	if fn != nil {
		return fn()
	}

	return nil, subsystems.NoSelector(), nil
}

type simpleMockStore struct {
	initialized bool
	flags       []ldstoretypes.KeyedItemDescriptor
	segments    []ldstoretypes.KeyedItemDescriptor
}

func (s simpleMockStore) IsInitialized() bool {
	return s.initialized
}

func (s simpleMockStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	switch kind {
	case ldstoreimpl.Features():
		return s.flags, nil
	case ldstoreimpl.Segments():
		return s.segments, nil
	default:
		return nil, nil
	}
}

func (s simpleMockStore) Snapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
	result := map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
		ldstoreimpl.Features(): s.flags,
		ldstoreimpl.Segments(): s.segments,
	}

	return result, subsystems.NoSelector(), nil
}

func makeMockStore(
	flags []ldmodel.FeatureFlag,
	segments []ldmodel.Segment,
) simpleMockStore {
	ret := simpleMockStore{initialized: true}
	for _, f := range flags {
		var item interface{} = &f
		if f.Deleted {
			item = nil
		}
		ret.flags = append(ret.flags, ldstoretypes.KeyedItemDescriptor{
			Key: f.Key, Item: ldstoretypes.ItemDescriptor{Version: f.Version, Item: item},
		})
	}
	for _, s := range segments {
		var item interface{} = &s
		if s.Deleted {
			item = nil
		}
		ret.segments = append(ret.segments, ldstoretypes.KeyedItemDescriptor{
			Key: s.Key, Item: ldstoretypes.ItemDescriptor{Version: s.Version, Item: item},
		})
	}
	return ret
}
