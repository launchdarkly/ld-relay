package streams

import (
	"errors"

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
	initialized       bool
	fakeFlagsError    error
	fakeSegmentsError error
	flags             []ldstoretypes.KeyedItemDescriptor
	segments          []ldstoretypes.KeyedItemDescriptor
}

func (q mockStoreQueries) IsInitialized() bool {
	return q.initialized
}

func (q mockStoreQueries) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	switch kind {
	case ldstoreimpl.Features():
		return q.flags, q.fakeFlagsError
	case ldstoreimpl.Segments():
		return q.segments, q.fakeSegmentsError
	default:
		return nil, nil
	}
}

func makeMockStore(flags []ldmodel.FeatureFlag, segments []ldmodel.Segment) mockStoreQueries {
	ret := mockStoreQueries{initialized: true}
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
