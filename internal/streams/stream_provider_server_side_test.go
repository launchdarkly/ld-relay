package streams

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"

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
		store := newMockStoreQueries()
		store.setupGetAllFn(func(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
			if kind == ldstoreimpl.Features() {
				return nil, fakeError
			}
			return nil, nil
		})

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.Register(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("initial event - store error for segments", func(t *testing.T) {
		store := newMockStoreQueries()
		store.setupGetAllFn(func(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
			if kind == ldstoreimpl.Segments() {
				return nil, fakeError
			}
			return nil, nil
		})

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
		const flagKey = "flagkey"

		expectReplayedEvents := func(t *testing.T, eventChannel <-chan eventsource.Event) []eventsource.Event {
			out := make([]eventsource.Event, 0)
			for {
				select {
				case e, ok := <-eventChannel:
					if !ok {
						return out // channel was closed; this is expected after the last event
					}
					out = append(out, e)
				case <-time.After(time.Second):
					require.Fail(t, "timed out waiting for replayed event (channel was not closed)")
				}
			}
		}

		queryThatIncrementsFlagVersionOnEachCall := func() func(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
			nextVersion := 1
			return func(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
				if kind != ldstoreimpl.Features() {
					return nil, nil
				}
				flag := ldbuilders.NewFlagBuilder("flagkey").Version(nextVersion).Build()
				nextVersion++
				return []ldstoretypes.KeyedItemDescriptor{
					{Key: flag.Key, Item: sharedtest.FlagDesc(flag)},
				}, nil
			}
		}

		getFlagFromEventData := func(t *testing.T, e eventsource.Event) ldmodel.FeatureFlag {
			require.Equal(t, "put", e.Event())
			var data struct {
				Data struct {
					Flags map[string]ldmodel.FeatureFlag `json:"flags"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(e.Data()), &data))
			require.Contains(t, data.Data.Flags, flagKey)
			return data.Data.Flags[flagKey]
		}

		t.Run("second client connects after first computation is done", func(t *testing.T) {
			store := newMockStoreQueries()
			store.setupGetAllFn(queryThatIncrementsFlagVersionOnEachCall())
			repo := &serverSideEnvStreamRepository{store: store, loggers: ldlog.NewDisabledLoggers()}

			eventCh1 := repo.Replay("", "")
			events1 := expectReplayedEvents(t, eventCh1)
			require.Len(t, events1, 1)

			eventCh2 := repo.Replay("", "")
			events2 := expectReplayedEvents(t, eventCh2)
			require.Len(t, events2, 1)

			assert.Equal(t, 1, getFlagFromEventData(t, events1[0]).Version)
			assert.Equal(t, 2, getFlagFromEventData(t, events2[0]).Version) // two separate computations were done
		})

		t.Run("second client connects while first computation is still in progress", func(t *testing.T) {
			underlyingQuery := queryThatIncrementsFlagVersionOnEachCall()
			replayStarted := make(chan struct{}, 2)
			replayCanFinish := make(chan struct{}, 1)
			var gateFirstReplay sync.Once
			store := newMockStoreQueries()
			store.setupGetAllFn(func(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
				if kind != ldstoreimpl.Features() {
					return nil, nil
				}
				replayStarted <- struct{}{}
				ret, err := underlyingQuery(kind)
				gateFirstReplay.Do(func() {
					<-replayCanFinish
				})

				time.Sleep(time.Millisecond * 200)
				// This delay is arbitrary and possibly overly timing-sensitive, but it looks like there is no
				// way to really guarantee that the goroutine for the second Replay has started before we allow
				// the first one to complete, without adding just-for-tests instrumentation inside of
				// serverSideEnvStreamRepository.getReplayEvent().

				return ret, err
			})
			repo := &serverSideEnvStreamRepository{store: store, loggers: ldlog.NewDisabledLoggers()}

			eventCh1 := repo.Replay("", "")
			<-replayStarted
			eventCh2 := repo.Replay("", "")
			replayCanFinish <- struct{}{}

			events1 := expectReplayedEvents(t, eventCh1)
			require.Len(t, events1, 1)

			events2 := expectReplayedEvents(t, eventCh2)
			require.Len(t, events2, 1)

			assert.Equal(t, 1, getFlagFromEventData(t, events1[0]).Version)
			assert.Equal(t, 1, getFlagFromEventData(t, events2[0]).Version) // only one computation was done
		})
	})
}
