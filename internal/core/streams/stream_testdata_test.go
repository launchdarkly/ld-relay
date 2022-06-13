package streams

import (
	"errors"
	"sync"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
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
	allData             = []ldstoretypes.Collection{
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
	getAllFn        func(ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error)
	lock            sync.Mutex
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

func (q *mockStoreQueries) setupGetAllFn(fn func(ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error)) {
	q.lock.Lock()
	q.getAllFn = fn
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

func (q *mockStoreQueries) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	q.lock.Lock()
	fn := q.getAllFn
	q.lock.Unlock()
	if fn != nil {
		return fn(kind)
	}
	return nil, nil
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

func makeMockStore(flags []ldmodel.FeatureFlag, segments []ldmodel.Segment) simpleMockStore {
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

func readAllEvents(ch <-chan eventsource.Event) []eventsource.Event {
	var ret []eventsource.Event
	for e := range ch {
		ret = append(ret, e)
	}
	return ret
}
