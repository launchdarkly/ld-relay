package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

const (
	testSDKKey    = config.SDKKey("sdk-key")
	testMobileKey = config.MobileKey("mobile-key")
	testEnvID     = config.EnvironmentID("env-id")
)

var (
	fakeError    = errors.New("sorry")
	testFlag1    = ldbuilders.NewFlagBuilder("flag1").Version(1).On(true).Build()
	testFlag2    = ldbuilders.NewFlagBuilder("flag2").Version(1).On(false).Build()
	testSegment1 = ldbuilders.NewSegmentBuilder("segment1").Version(1).Build()
	allData      = []ldstoretypes.Collection{
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

type mockStore struct {
	realStore        interfaces.DataStore
	fakeError        error
	statusMonitoring bool
	closed           bool
}

type mockStoreFactory struct {
	instance             interfaces.DataStore
	fakeError            error
	receivedContext      interfaces.ClientContext
	receivedStoreUpdates interfaces.DataStoreUpdates
}

type mockEnvStreamsUpdates struct {
	allData    [][]ldstoretypes.Collection
	singleItem []sharedtest.ReceivedItemUpdate
}

func (s *mockStore) Init(allData []ldstoretypes.Collection) error {
	if s.fakeError != nil {
		return s.fakeError
	}
	return s.realStore.Init(allData)
}

func (s *mockStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	if s.fakeError != nil {
		return ldstoretypes.ItemDescriptor{}, s.fakeError
	}
	return s.realStore.Get(kind, key)
}

func (s *mockStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	if s.fakeError != nil {
		return nil, s.fakeError
	}
	return s.realStore.GetAll(kind)
}

func (s *mockStore) Upsert(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) (bool, error) {
	if s.fakeError != nil {
		return false, s.fakeError
	}
	return s.realStore.Upsert(kind, key, item)
}

func (s *mockStore) IsInitialized() bool {
	return s.realStore.IsInitialized()
}

func (s *mockStore) IsStatusMonitoringEnabled() bool {
	return s.statusMonitoring
}

func (s *mockStore) Close() error {
	s.closed = true
	return s.realStore.Close()
}

func (f *mockStoreFactory) CreateDataStore(
	context interfaces.ClientContext,
	storeUpdates interfaces.DataStoreUpdates,
) (interfaces.DataStore, error) {
	f.receivedContext = context
	f.receivedStoreUpdates = storeUpdates
	if f.fakeError != nil {
		return nil, f.fakeError
	}
	return f.instance, nil
}

func (m *mockEnvStreamsUpdates) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	m.allData = append(m.allData, allData)
}

func (m *mockEnvStreamsUpdates) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	m.singleItem = append(m.singleItem, sharedtest.ReceivedItemUpdate{kind, key, item})
}

func (m *mockEnvStreamsUpdates) expectAllDataUpdate(t *testing.T) []ldstoretypes.Collection {
	switch {
	case len(m.allData) == 1:
		return m.allData[0]
	case len(m.allData) > 1:
		require.Fail(t, "received multiple updates, expected only one")
	default:
		require.Fail(t, "did not receive expected update")
	}
	return nil
}

func (m *mockEnvStreamsUpdates) expectItemUpdate(t *testing.T) sharedtest.ReceivedItemUpdate {
	switch {
	case len(m.singleItem) == 1:
		return m.singleItem[0]
	case len(m.singleItem) > 1:
		require.Fail(t, "received multiple updates, expected only one")
	default:
		require.Fail(t, "did not receive expected update")
	}
	return sharedtest.ReceivedItemUpdate{}
}

func (m *mockEnvStreamsUpdates) expectNoAllDataUpdate(t *testing.T) {
	if len(m.allData) != 0 {
		require.Fail(t, "expected no update", "received: %+v", m.allData)
	}
}

func (m *mockEnvStreamsUpdates) expectNoItemUpdate(t *testing.T) {
	if len(m.singleItem) != 0 {
		require.Fail(t, "expected no update", "received: %+v", m.singleItem)
	}
}
