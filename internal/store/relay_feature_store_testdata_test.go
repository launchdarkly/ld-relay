package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/require"
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
	testFlag1JSON, _    = json.Marshal(testFlag1)
	testSegment1JSON, _ = json.Marshal(testSegment1)

	allDataChanges = []subsystems.Change{
		{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Object: testFlag1JSON},
		{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Object: testSegment1JSON},
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

type mockStore struct {
	realStore        subsystems.DataStore
	fakeError        error
	statusMonitoring bool
	closed           bool
}

type mockStoreFactory struct {
	instance             subsystems.DataStore
	fakeError            error
	receivedContext      subsystems.ClientContext
	receivedStoreUpdates subsystems.DataStoreUpdateSink
}

type mockEnvStreamsUpdates struct {
	allData    [][]subsystems.Change
	singleItem []subsystems.Change
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

func (f *mockStoreFactory) Build(
	context subsystems.ClientContext,
) (subsystems.DataStore, error) {
	f.receivedContext = context
	f.receivedStoreUpdates = context.GetDataStoreUpdateSink()
	if f.fakeError != nil {
		return nil, f.fakeError
	}
	return f.instance, nil
}

func (m *mockEnvStreamsUpdates) Apply(changeSet subsystems.ChangeSet) {
	switch changeSet.IntentCode() {
	case subsystems.IntentTransferFull:
		m.allData = append(m.allData, changeSet.Changes())
	case subsystems.IntentTransferChanges:
		m.singleItem = append(m.singleItem, changeSet.Changes()...)
	}
}

func (m *mockEnvStreamsUpdates) InvalidateClientSideState() {}

func (m *mockEnvStreamsUpdates) expectAllDataUpdate(t *testing.T) []subsystems.Change {
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

func (m *mockEnvStreamsUpdates) expectItemUpdate(t *testing.T) subsystems.Change {
	switch {
	case len(m.singleItem) == 1:
		return m.singleItem[0]
	case len(m.singleItem) > 1:
		require.Fail(t, "received multiple updates, expected only one")
	default:
		require.Fail(t, "did not receive expected update")
	}
	return subsystems.Change{}
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
