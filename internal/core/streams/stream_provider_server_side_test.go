package streams

import (
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamProviderServerSide(t *testing.T) {
	validCredential := testSDKKey
	invalidCredential1 := testMobileKey
	invalidCredential2 := testEnvID

	withStreamProvider := func(t *testing.T, maxConnTime time.Duration, action func(StreamProvider)) {
		sp := NewStreamProvider(basictypes.ServerSideStream, maxConnTime)
		require.NotNil(t, sp)
		defer sp.Close()
		action(sp)
	}

	t.Run("constructor", func(t *testing.T) {
		maxConnTime := time.Hour
		withStreamProvider(t, maxConnTime, func(sp StreamProvider) {
			require.IsType(t, &serverSideStreamProvider{}, sp)
			verifyServerProperties(t, sp.(*serverSideStreamProvider).server, maxConnTime)
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
			require.IsType(t, &serverSideEnvStreamProvider{}, esp)
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

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSidePutEvent(allData))
		})
	})

	t.Run("initial event - omits deleted items", func(t *testing.T) {
		testFlag1Deleted := testFlag1
		testFlag1Deleted.Deleted = true
		testSegment1Deleted := testSegment1
		testSegment1Deleted.Deleted = true
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1Deleted, testFlag2}, []ldmodel.Segment{testSegment1Deleted})
		storeWithoutDeleted := makeMockStore([]ldmodel.FeatureFlag{testFlag2}, []ldmodel.Segment{})
		allDataWithoutDeleted := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: storeWithoutDeleted.flags},
			{Kind: ldstoreimpl.Segments(), Items: storeWithoutDeleted.segments},
		}
		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSidePutEvent(allDataWithoutDeleted))
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

	t.Run("initial event - store error for segments", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		store.fakeSegmentsError = fakeError

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

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.SendAllDataUpdate(newData)
				},
				MakeServerSidePutEvent(newData),
			)
		})
	})

	t.Run("SendSingleItemUpdate", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
				},
				MakeServerSidePatchEvent(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))
				},
				MakeServerSidePatchEvent(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1)),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.DeletedItem(1))
				},
				MakeServerSideDeleteEvent(ldstoreimpl.Features(), testFlag1.Key, 1),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.DeletedItem(1))
				},
				MakeServerSideDeleteEvent(ldstoreimpl.Segments(), testSegment1.Key, 1),
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

	t.Run("Replay", func(t *testing.T) {
		store := makeMockStore(nil, nil)
		store.latency = 100 * time.Millisecond

		repo := &serverSideEnvStreamRepository{store: store, loggers: ldlog.NewDisabledLoggers()}

		newData := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: store.flags},
			{Kind: ldstoreimpl.Segments(), Items: store.segments},
		}
		expected := MakeServerSidePutEvent(newData)
		expectedData := expected.Data()
		t.Run("ConsecutiveCalls", func(t *testing.T) {
			eventChannel := repo.Replay("", "")
			select {
			case actual := <-eventChannel:
				if actual == nil {
					t.Fatal("Expected an event from Replay")
				}
				if expectedData != actual.Data() {
					t.Errorf("Replay() = %v; wanted %v", actual.Data(), expectedData)
				}
			case <-time.After(time.Second):
				t.Error("expected an event to be returned")
			}
			eventChannel = repo.Replay("", "")
			select {
			case actual := <-eventChannel:
				if actual == nil {
					t.Fatal("Expected an event from Replay")
				}
				if expectedData != actual.Data() {
					t.Errorf("Replay() = %v; wanted %v", actual.Data(), expectedData)
				}
			case <-time.After(time.Second):
				t.Error("expected an event to be returned")
			}

		})
		t.Run("ConcurrentCalls", func(t *testing.T) {
			eventChannel1 := repo.Replay("", "")
			eventChannel2 := repo.Replay("", "")
			var event1, event2 eventsource.Event
			var events int

			for events < 2 {
				select {
				case event, ok := <-eventChannel1:
					if !ok {
						break
					}
					if event == nil {
						t.Fatal("Expected an event from Replay")
					}
					if expectedData != event.Data() {
						t.Errorf("Replay() = %v; wanted %v", event.Data(), expectedData)
					}
					event1 = event
					events++
				case event, ok := <-eventChannel2:
					if !ok {
						break
					}
					if event == nil {
						t.Fatal("Expected an event from Replay")
					}
					if expectedData != event.Data() {
						t.Errorf("Replay() = %v; wanted %v", event.Data(), expectedData)
					}
					event2 = event
					events++
				case <-time.After(time.Second):
					t.Error("expected an event to be returned")
				}
			}
			if event1 != event2 {
				t.Error("Expected the same exact event to be returned from the flightgroup")
			}
		})
	})
}
