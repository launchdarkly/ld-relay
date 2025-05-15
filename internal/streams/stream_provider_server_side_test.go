package streams

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamProviderServerSide(t *testing.T) {
	validCredential := sdkauth.New(testSDKKey)
	invalidCredential1 := sdkauth.New(testMobileKey)
	invalidCredential2 := sdkauth.New(testEnvID)

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
			verifyServerProperties(t, sp.(*serverSideStreamProvider).fdv1Server, maxConnTime)
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
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
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
		store := makeMockStore(
			[]ldmodel.FeatureFlag{testFlag1Deleted, testFlag2},
			[]ldmodel.Segment{testSegment1Deleted},
		)
		storeWithoutDeleted := makeMockStore([]ldmodel.FeatureFlag{testFlag2}, []ldmodel.Segment{})
		allDataWithoutDeleted := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: storeWithoutDeleted.flags},
			{Kind: ldstoreimpl.Segments(), Items: storeWithoutDeleted.segments},
		}
		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSidePutEvent(allDataWithoutDeleted))
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

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.SetBasis(changes, subsystems.Selector{})
				},
				MakeServerSidePutEvent(newData),
			)
		})
	})

	t.Run("ApplyDelta", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, ldlog.NewDisabledLoggers())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: testFlag1.Version, Object: testFlag1JSON},
					}, subsystems.NoSelector())
				},
				MakeServerSidePatchEvent(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypePut, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: testSegment1.Version, Object: testSegment1JSON},
					}, subsystems.NoSelector())
				},
				MakeServerSidePatchEvent(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1)),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypeDelete, Kind: subsystems.FlagKind, Key: testFlag1.Key, Version: testFlag1.Version},
					}, subsystems.NoSelector())
				},
				MakeServerSideDeleteEvent(ldstoreimpl.Features(), testFlag1.Key, testFlag1.Version),
			)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() {
					esp.ApplyDelta([]subsystems.Change{
						{Action: subsystems.ChangeTypeDelete, Kind: subsystems.SegmentKind, Key: testSegment1.Key, Version: testSegment1.Version},
					}, subsystems.NoSelector())
				},
				MakeServerSideDeleteEvent(ldstoreimpl.Segments(), testSegment1.Key, testSegment1.Version),
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

	t.Run("Replay", func(t *testing.T) {
		const flagKey = "flagkey"

		expectReplayedEvents := func(t *testing.T, eventChannel <-chan eventsource.Event) []eventsource.Event {
			out := make([]eventsource.Event, 0)
			for {
				e, ok, closed := helpers.TryReceive(eventChannel, time.Second)
				if closed {
					return out // channel was closed; this is expected after the last event
				}
				if !ok {
					require.Fail(t, "timed out waiting for replayed event (channel was not closed)")
				}
				out = append(out, e)
			}
		}

		queryThatIncrementsFlagVersionOnEachCall := func() func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
			nextVersion := 1
			return func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
				flag := ldbuilders.NewFlagBuilder("flagkey").Version(nextVersion).Build()
				nextVersion++
				return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
					ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
					ldstoreimpl.Segments(): {},
				}, subsystems.NoSelector(), nil
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
			store.setupSnapshotFn(queryThatIncrementsFlagVersionOnEachCall())
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
			store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
				replayStarted <- struct{}{}
				ret, selector, err := underlyingQuery()
				gateFirstReplay.Do(func() {
					<-replayCanFinish
				})

				time.Sleep(time.Millisecond * 200)
				// This delay is arbitrary and possibly overly timing-sensitive, but it looks like there is no
				// way to really guarantee that the goroutine for the second Replay has started before we allow
				// the first one to complete, without adding just-for-tests instrumentation inside of
				// serverSideEnvStreamRepository.getReplayEvent().

				return ret, selector, err
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
