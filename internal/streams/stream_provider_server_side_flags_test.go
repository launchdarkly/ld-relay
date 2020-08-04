package streams

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"

	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
)

func TestStreamProviderServerSideFlagsOnly(t *testing.T) {
	validCredential := testSDKKey
	invalidCredential1 := testMobileKey
	invalidCredential2 := testEnvID

	withStreamProvider := func(t *testing.T, maxConnTime time.Duration, action func(StreamProvider)) {
		sp := NewServerSideFlagsOnlyStreamProvider(maxConnTime)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &serverSideFlagsOnlyStreamProvider{}, sp)
			verifyServerProperties(t, sp.(*serverSideFlagsOnlyStreamProvider).server, maxConnTime)
		})
	})

	t.Run("Handler", func(t *testing.T) {
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.NotNil(t, sp.Handler(validCredential))
			assert.Nil(t, sp.Handler(invalidCredential1))
			assert.Nil(t, sp.Handler(invalidCredential2))
		})
	})

	t.Run("Register", func(t *testing.T) {
		store := makeMockStore(nil, nil)
		withStreamProvider(t, 0, func(sp StreamProvider) {
			assert.Nil(t, sp.Register(invalidCredential1, store, ldlog.NewDisabledLoggers()))
			assert.Nil(t, sp.Register(invalidCredential2, store, ldlog.NewDisabledLoggers()))

			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
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
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(allData))
		})
	})

	t.Run("initial event - store not initialized", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		store.initialized = false

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("initial event - store error for flags", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		store.fakeFlagsError = fakeError

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("SendAllDataUpdate", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			newData := []ldstoretypes.Collection{
				{Kind: ldstoreimpl.Features(), Items: store.flags},
				{Kind: ldstoreimpl.Segments(), Items: store.segments},
			}

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.SendAllDataUpdate(newData)
				},
				MakeServerSideFlagsOnlyPutEvent(newData),
			)
		})
	})

	t.Run("SendSingleItemUpdate", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
				},
				MakeServerSideFlagsOnlyPatchEvent(testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))
				},
				nil,
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.DeletedItem(1))
				},
				MakeServerSideFlagsOnlyDeleteEvent(testFlag1.Key, 1),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSideFlagsOnlyPutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.DeletedItem(1))
				},
				nil,
			)
		})
	})

	t.Run("Heartbeat", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerHeartbeat(t, sp, esp, validCredential)
		})
	})
}
