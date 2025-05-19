package streams

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamProviderServerSideFlagsOnly(t *testing.T) {
	validCredential := sdkauth.New(testSDKKey)
	invalidCredential1 := sdkauth.New(testMobileKey)
	invalidCredential2 := sdkauth.New(testEnvID)

	withStreamProvider := func(t *testing.T, maxConnTime time.Duration, action func(StreamProvider)) {
		sp := NewStreamProvider(basictypes.ServerSideFlagsOnlyStream, maxConnTime)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &serverSideFlagsOnlyStreamProvider{}, sp)
			verifyServerProperties(t, sp.(*serverSideFlagsOnlyStreamProvider).fdv1Server, maxConnTime)
			verifyServerProperties(t, sp.(*serverSideFlagsOnlyStreamProvider).fdv2Server, maxConnTime)
		})
	})

	t.Run("Handler", func(t *testing.T) {
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.NotNil(t, sp.HandlerV1(validCredential))
			assert.Nil(t, sp.HandlerV1(invalidCredential1))
			assert.Nil(t, sp.HandlerV1(invalidCredential2))
		})
	})

	t.Run("Register", func(t *testing.T) {
		store := makeMockStore(nil, nil)
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.Nil(t, sp.RegisterV1(invalidCredential1, store, ldlog.NewDisabledLoggers()))
			assert.Nil(t, sp.RegisterV1(invalidCredential2, store, ldlog.NewDisabledLoggers()))

			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()
			require.IsType(t, &serverSideFlagsOnlyEnvStreamProvider{}, esp)
		})
	})

	t.Run("initial event", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		allData := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: store.flags},
			{Kind: ldstoreimpl.Segments(), Items: store.segments},
		}

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(allData))
		})
	})

	t.Run("initial event - omits deleted items", func(t *testing.T) {
		testFlag1Deleted := testFlag1
		testFlag1Deleted.Deleted = true
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1Deleted, testFlag2}, []ldmodel.Segment{testSegment1})
		storeWithoutDeleted := makeMockStore([]ldmodel.FeatureFlag{testFlag2}, []ldmodel.Segment{testSegment1})
		allDataWithoutDeleted := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: storeWithoutDeleted.flags},
			{Kind: ldstoreimpl.Segments(), Items: storeWithoutDeleted.segments},
		}
		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(allDataWithoutDeleted))
		})
	})

	t.Run("initial event - store not initialized", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		store.initialized = false

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("initial event - store error", func(t *testing.T) {
		store := newMockStoreQueries()
		store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
			return nil, subsystems.NoSelector(), fakeError
		})

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("SetBasis", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			changes := []subsystems.Change{
				{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: 1, Object: testFlag1JSON},
				{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: 1, Object: testSegment1JSON},
			}

			newData := []ldstoretypes.Collection{
				{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{
					{Key: testFlag1.Key, Item: sharedtest.FlagDesc(testFlag1)},
				}},
				{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{
					{Key: testSegment1.Key, Item: sharedtest.SegmentDesc(testSegment1)},
				}},
			}

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.SetBasis(changes, subsystems.Selector{})
				},
				MakeServerSideFlagsOnlyPutEvent(newData),
			)
		})
	})

	t.Run("ApplyDelta", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: testFlag1.Version, Object: testFlag1JSON},
					}, subsystems.NoSelector())
				},
				MakeServerSideFlagsOnlyPatchEvent(testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: testSegment1.Version, Object: testSegment1JSON},
					}, subsystems.NoSelector())
				},
				nil,
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypeDelete, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: testFlag1.Version},
					}, subsystems.NoSelector())
				},
				MakeServerSideFlagsOnlyDeleteEvent(testFlag1.Key, testFlag1.Version),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypeDelete, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: testSegment1.Version},
					}, subsystems.NoSelector())
				},
				nil,
			)
		})
	})

	t.Run("Heartbeat", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerHeartbeat(t, sp, esp, validCredential)
		})
	})
}
