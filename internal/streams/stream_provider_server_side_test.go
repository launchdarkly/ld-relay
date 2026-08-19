package streams

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/eventsource"
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
		sp := NewStreamProvider(basictypes.ServerSideStream, maxConnTime, 0)
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
			assert.Nil(t, sp.RegisterV1(invalidCredential1, store, slog.Default()))
			assert.Nil(t, sp.RegisterV1(invalidCredential2, store, slog.Default()))

			esp := sp.RegisterV1(validCredential, store, slog.Default())
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
			esp := sp.RegisterV1(validCredential, store, slog.Default())
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
			esp := sp.RegisterV1(validCredential, store, slog.Default())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, MakeServerSidePutEvent(allDataWithoutDeleted))
		})
	})

	t.Run("initial event - store not initialized", func(t *testing.T) {
		store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
		store.initialized = false

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, slog.Default())
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
			esp := sp.RegisterV1(validCredential, store, slog.Default())
			require.NotNil(t, esp)
			defer esp.Close()

			verifyHandlerInitialEvent(t, sp, validCredential, nil)
		})
	})

	t.Run("SetBasis", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, slog.Default())
			require.NotNil(t, esp)
			defer esp.Close()

			changeSet, err := subsystems.NewChangeSetBuilder().
				Start(subsystems.ServerIntent{
					Payload: subsystems.Payload{
						ID:     "state",
						Target: 1,
						Code:   subsystems.IntentTransferFull,
						Reason: "cant-catchup",
					},
				}).
				AddPut(subsystems.FlagKind, testFlag1.Key, 1, testFlag1JSON).
				AddPut(subsystems.SegmentKind, testSegment1.Key, 1, testSegment1JSON).
				Finish(subsystems.NewSelector("state", 1))
			require.NoError(t, err)

			newData := []ldstoretypes.Collection{
				{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{
					{Key: testFlag1.Key, Item: sharedtest.FlagDesc(testFlag1)},
				}},
				{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{
					{Key: testSegment1.Key, Item: sharedtest.SegmentDesc(testSegment1)},
				}},
			}

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() { esp.Apply(*changeSet) },
				MakeServerSidePutEvent(newData),
			)
		})
	})

	t.Run("ApplyDelta", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, slog.Default())
			require.NotNil(t, esp)
			defer esp.Close()

			changeSetBuilder := subsystems.NewChangeSetBuilder()
			flagChangeSet, err := changeSetBuilder.
				Start(subsystems.ServerIntent{
					Payload: subsystems.Payload{
						ID:     "state",
						Target: 1,
						Code:   subsystems.IntentTransferChanges,
						Reason: "stale",
					},
				}).
				AddPut(subsystems.FlagKind, testFlag1.Key, 1, testFlag1JSON).
				Finish(subsystems.NewSelector("state", 1))
			require.NoError(t, err)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() { esp.Apply(*flagChangeSet) },
				MakeServerSidePatchEvent(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
			)

			assert.NoError(t, changeSetBuilder.ExpectChanges())

			segmentChangeSet, err := changeSetBuilder.
				AddPut(subsystems.SegmentKind, testSegment1.Key, 1, testSegment1JSON).
				Finish(subsystems.NewSelector("state", 2))
			require.NoError(t, err)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() { esp.Apply(*segmentChangeSet) },
				MakeServerSidePatchEvent(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1)),
			)

			assert.NoError(t, changeSetBuilder.ExpectChanges())

			deleteFlagChangeSet, err := changeSetBuilder.
				AddDelete(subsystems.FlagKind, testFlag1.Key, testFlag1.Version).
				Finish(subsystems.NewSelector("state", 3))
			require.NoError(t, err)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() { esp.Apply(*deleteFlagChangeSet) },
				MakeServerSideDeleteEvent(ldstoreimpl.Features(), testFlag1.Key, testFlag1.Version),
			)

			assert.NoError(t, changeSetBuilder.ExpectChanges())

			deleteSegmentChangeSet, err := changeSetBuilder.
				AddDelete(subsystems.SegmentKind, testSegment1.Key, testSegment1.Version).
				Finish(subsystems.NewSelector("state", 4))
			require.NoError(t, err)

			verifyHandlerUpdateEvent(t, sp, validCredential, MakeServerSidePutEvent(nil),
				func() { esp.Apply(*deleteSegmentChangeSet) },
				MakeServerSideDeleteEvent(ldstoreimpl.Segments(), testSegment1.Key, testSegment1.Version),
			)
		})
	})

	t.Run("Heartbeat", func(t *testing.T) {
		store := makeMockStore(nil, nil)

		withStreamProvider(t, 0, func(sp StreamProvider) {
			esp := sp.RegisterV1(validCredential, store, slog.Default())
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
			repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default()}

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
			repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default()}

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

		t.Run("v2 clients with different basis values connect concurrently", func(t *testing.T) {
			const currentState = "current-state"
			flag := ldbuilders.NewFlagBuilder(flagKey).Version(1).Build()
			replayStarted := make(chan struct{}, 2)
			replayCanFinish := make(chan struct{})
			var gateFirstReplay sync.Once
			store := newMockStoreQueries()
			store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
				replayStarted <- struct{}{}
				gateFirstReplay.Do(func() {
					<-replayCanFinish
				})
				return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
					ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
					ldstoreimpl.Segments(): {},
				}, subsystems.NewSelector(currentState, 1), nil
			})
			repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true}

			// The first client's basis matches the store's current state, so its replay should be
			// an "up-to-date" intent with no data.
			eventCh1 := repo.Replay("", currentState)
			<-replayStarted

			// The second client has no basis, so it needs a full data transfer. Its result must
			// not be shared with the first client's, even though the two replays are concurrent.
			eventCh2 := repo.Replay("", "")

			time.Sleep(time.Millisecond * 200)
			// This delay gives the second replay's goroutine time to reach the flight group while
			// the first computation is still in progress, which is the scenario under test. (See
			// the comment in the previous subtest about timing sensitivity.)

			close(replayCanFinish)

			events1 := expectReplayedEvents(t, eventCh1)
			require.Len(t, events1, 1)
			assert.Equal(t, string(subsystems.EventServerIntent), events1[0].Event())
			assert.Contains(t, events1[0].Data(), `"intentCode":"none"`)

			events2 := expectReplayedEvents(t, eventCh2)
			eventNames := make([]string, 0, len(events2))
			for _, e := range events2 {
				eventNames = append(eventNames, e.Event())
			}
			assert.Equal(t, []string{
				string(subsystems.EventServerIntent),
				string(subsystems.EventPutObject),
				string(subsystems.EventPayloadTransferred),
			}, eventNames)
		})

		t.Run("ReplayWithContext stops producing when the subscriber's context is cancelled", func(t *testing.T) {
			// A subscriber that disconnects mid-replay cancels the request context. The producer must
			// stop sending promptly instead of blocking forever on a send that nobody will receive.
			snapshotReturned := make(chan struct{}, 1)
			underlyingQuery := queryThatIncrementsFlagVersionOnEachCall()
			store := newMockStoreQueries()
			store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
				data, selector, err := underlyingQuery()
				snapshotReturned <- struct{}{}
				return data, selector, err
			})
			repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default()}

			ctx, cancel := context.WithCancel(context.Background())
			eventCh := repo.ReplayWithContext(ctx, "", "")

			// Wait until the producer has computed the events and is parked on its (unbuffered,
			// unread) send, then cancel without ever consuming an event.
			<-snapshotReturned
			time.Sleep(50 * time.Millisecond)
			cancel()

			// Let the producer observe cancellation while no receiver exists: its select then has
			// only the ctx.Done case ready, so it must exit without delivering anything. Only then
			// attach a receiver. Asserting closed-with-no-event on the first receive is what makes
			// this test fail against a producer that ignores the context — such a producer would be
			// rescued by the receive, deliver its event, and only then close the channel.
			time.Sleep(50 * time.Millisecond)
			_, ok, closed := helpers.TryReceive(eventCh, time.Second)
			require.False(t, ok, "producer delivered an event after cancellation")
			require.True(t, closed, "producer did not stop after context cancellation (channel never closed)")
		})
	})
}

func TestStreamReplayShedClosesConnectionWhenBudgetFull(t *testing.T) {
	const flagKey = "flagkey"
	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(1).Build()
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NoSelector(), nil
	})

	// A budget with its only slot already held, so the replay must shed.
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	release, ok := limiter.Acquire(context.Background())
	require.True(t, ok)
	defer release()

	repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), initLimiter: limiter}

	// The connection's close hook, as withInitDeadline would install it. A shed replay must
	// invoke it so the SDK reconnects instead of stranding on an open, uninitialized stream.
	closed := make(chan struct{})
	ctx := context.WithValue(context.Background(), closeConnectionKey{}, func() { close(closed) })

	eventCh := repo.ReplayWithContext(ctx, "", "")

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		require.Fail(t, "shed replay did not close the connection")
	}

	// It also delivers no events: the channel is closed with nothing sent.
	select {
	case e, ok := <-eventCh:
		assert.False(t, ok, "shed replay must deliver no events (got %v)", e)
	case <-time.After(2 * time.Second):
		require.Fail(t, "replay channel was not closed after shed")
	}
}

// TestStreamReplayQueuedClientDisconnectRelinquishesQueueSpot pins the intended exit from
// the queue: a client that disconnects while it waits for a slot releases its place
// immediately, and the SDK then tries again on its own backoff schedule. The exit must not
// write a warning, and the occupancy must come back, so a later client can use the queue.
func TestStreamReplayQueuedClientDisconnectRelinquishesQueueSpot(t *testing.T) {
	const flagKey = "flagkey"
	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(1).Build()
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NoSelector(), nil
	})

	waitForWaiting := func(n int, l *concurrency.Limiter) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for l.Stats().Waiting != n {
			if time.Now().After(deadline) {
				require.Failf(t, "timeout", "limiter never reported Waiting==%d (stats: %+v)", n, l.Stats())
			}
			runtime.Gosched()
		}
	}

	// The only slot is held, so the replay must queue.
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	release, ok := limiter.Acquire(context.Background())
	require.True(t, ok)

	var recs []slog.Record
	repo := &serverSideEnvStreamRepository{
		store: store, logger: slog.New(capturingHandler{&recs}), initLimiter: limiter,
	}

	cctx, cancel := context.WithCancel(context.Background())
	eventCh := repo.ReplayWithContext(cctx, "", "")
	waitForWaiting(1, limiter)

	cancel() // the client disconnects while queued

	// The replay ends with nothing sent.
	select {
	case e, open := <-eventCh:
		assert.False(t, open, "a disconnected queued replay must deliver no events (got %v)", e)
	case <-time.After(2 * time.Second):
		require.Fail(t, "replay channel was not closed after the disconnect")
	}
	waitForWaiting(0, limiter)

	// The designed exit is quiet: no warning for a client that chose to leave.
	for _, rec := range recs {
		assert.NotEqual(t, slog.LevelWarn, rec.Level, "disconnect from the queue must not log a warning: %s", rec.Message)
	}

	// The occupancy came back with the queue spot: a holder plus a queued waiter fit again.
	admitted := make(chan bool)
	go func() {
		r2, ok2 := limiter.Acquire(context.Background())
		if ok2 {
			r2()
		}
		admitted <- ok2
	}()
	waitForWaiting(1, limiter)
	release()
	assert.True(t, <-admitted, "queue capacity was not relinquished by the disconnected client")
}

// TestStreamReplayShutdownEndsQuietly pins the shutdown path: a replay that the limiter
// rejects because it is closing must not write the budget-saturation warning. One line for
// each parked waiter would flood the log during a normal shutdown and look like an outage.
func TestStreamReplayShutdownEndsQuietly(t *testing.T) {
	const flagKey = "flagkey"
	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(1).Build()
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NoSelector(), nil
	})

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	limiter.Close()

	var recs []slog.Record
	repo := &serverSideEnvStreamRepository{
		store: store, logger: slog.New(capturingHandler{&recs}), initLimiter: limiter,
	}
	eventCh := repo.ReplayWithContext(context.Background(), "", "")
	select {
	case e, open := <-eventCh:
		assert.False(t, open, "a shutdown replay must deliver no events (got %v)", e)
	case <-time.After(2 * time.Second):
		require.Fail(t, "replay channel was not closed at shutdown")
	}
	for _, rec := range recs {
		assert.NotEqual(t, slog.LevelWarn, rec.Level,
			"shutdown must not log the budget-saturation warning: %s", rec.Message)
	}
}

// deadlineRecorder is a ResponseWriter whose recorded deadlines are safe to read after the
// handler returns. Tests use it to make sure the watcher completed its cut before the
// wrapper returned.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineRecorder) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.deadlines)
}

// TestWatcherCutCompletesBeforeHandlerReturns pins the watcher join: before the wrapped
// handler returns to net/http, the watcher must complete its cut. Without the join, the cut
// could touch a connection that net/http already gave to another request, which the writer
// contract forbids. The handler returns in the middle of a delivery, so the cut makes a
// real deadline call.
func TestWatcherCutCompletesBeforeHandlerReturns(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 4, MaxQueued: 4})
	sp := &serverSideStreamProvider{initLimiter: limiter, sendTimeout: 30 * time.Second}

	for i := 0; i < 100; i++ {
		rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		wrapped := sp.withInitDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			iw, ok := r.Context().Value(initWriterKey{}).(*initwrite.Writer)
			require.True(t, ok)
			iw.Begin()
			_, _ = w.Write([]byte("data: x\n\n"))
			// Return mid-delivery: the wrapper's deferred cancel fires the watcher's cut.
		}))
		wrapped.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		// One arm from the write, one deadline-to-now from the watcher's cut: both must have
		// happened strictly before ServeHTTP returned.
		if rec.count() != 2 {
			t.Fatalf("iteration %d: watcher had not completed its cut when the handler returned (%d deadline calls)", i, rec.count())
		}
	}
}

// TestStreamReplayCurrentBasisIsUpToDateDespiteConcurrentStaleRead guards the peek key: a
// client that reconnects at the CURRENT basis must get an up-to-date reply, also while the
// older-basis store read of another client is in progress. With one fixed key for all
// reads, this client joined the older read, failed the up-to-date check against the old
// selector, and incorrectly received a full basis at the old state.
// TestStreamReplayQueuedClientRefreshesItsReadAfterAdmission pins the post-admission read:
// a client queued while its basis was ahead of the store, and the store became equal to
// that basis during the wait. The client must get the small up-to-date reply, not a full
// basis serialized from the state that applied when it first asked. On the up-to-date path,
// the second read also returns the slot immediately.
func TestStreamReplayQueuedClientRefreshesItsReadAfterAdmission(t *testing.T) {
	const flagKey = "flagkey"
	var stateNow atomic.Int64
	stateNow.Store(1)

	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		st := int(stateNow.Load())
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(st).Build()
		sel := "s" + strconv.Itoa(st)
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NewSelector(sel, st), nil
	})

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	hold, ok := limiter.Acquire(context.Background()) // the only slot is held, so the client queues
	require.True(t, ok)

	repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true, initLimiter: limiter}

	// The client asks at basis "s2" while the store is at s1: not up-to-date, so it needs a
	// full basis and parks in the queue.
	eventCh := repo.ReplayWithContext(context.Background(), "", "s2")
	deadline := time.Now().Add(2 * time.Second)
	for limiter.Stats().Waiting != 1 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "client never queued (stats: %+v)", limiter.Stats())
		}
		runtime.Gosched()
	}

	// While it waits, the store catches up to the client's basis; then the slot frees.
	stateNow.Store(2)
	hold()

	var events []eventsource.Event
	for {
		e, ok, closed := helpers.TryReceive(eventCh, 2*time.Second)
		if closed {
			break
		}
		require.True(t, ok, "timed out waiting for replayed event")
		events = append(events, e)
	}
	require.Len(t, events, 1, "expected a single up-to-date event, got a full basis (the read was not refreshed after the queue wait)")
	assert.Equal(t, string(subsystems.EventServerIntent), events[0].Event())
	assert.Contains(t, events[0].Data(), `"intentCode":"none"`)

	// The up-to-date path returns the slot at once.
	deadline = time.Now().Add(2 * time.Second)
	for limiter.Stats().Held != 0 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "slot not released after the up-to-date reply (stats: %+v)", limiter.Stats())
		}
		runtime.Gosched()
	}
}

// TestStreamReplayQueuedFullBasisIsSerializedFromPostAdmissionRead pins the other half of
// the post-admission read: a client that continues to need a full basis after its queue
// wait must receive the store as it is at admission, not the state that applied when it
// first asked. A serialization from the pre-queue snapshot is the exact fault the second
// read exists to prevent.
func TestStreamReplayQueuedFullBasisIsSerializedFromPostAdmissionRead(t *testing.T) {
	const flagKey = "flagkey"
	var stateNow atomic.Int64
	stateNow.Store(1)

	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		st := int(stateNow.Load())
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(st).Build()
		sel := "s" + strconv.Itoa(st)
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NewSelector(sel, st), nil
	})

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	hold, ok := limiter.Acquire(context.Background())
	require.True(t, ok)

	repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true, initLimiter: limiter}

	// The client's basis "s9" never becomes current, so it needs a full basis both before and
	// after the wait.
	eventCh := repo.ReplayWithContext(context.Background(), "", "s9")
	deadline := time.Now().Add(2 * time.Second)
	for limiter.Stats().Waiting != 1 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "client never queued (stats: %+v)", limiter.Stats())
		}
		runtime.Gosched()
	}

	// The store advances while the client waits; then the slot frees.
	stateNow.Store(3)
	hold()

	var payload strings.Builder
	for {
		e, ok, closed := helpers.TryReceive(eventCh, 2*time.Second)
		if closed {
			break
		}
		require.True(t, ok, "timed out waiting for replayed event")
		payload.WriteString(e.Event())
		payload.WriteString(e.Data())
	}
	// The flag data is what ages: the selector travels separately, so a regression that
	// serializes the stale snapshot while rechecking the fresh selector still reports "s3".
	// Only the flag version proves which snapshot fed the serialization.
	assert.Contains(t, payload.String(), `"version":3`, "the basis must be serialized from the post-admission read")
	assert.NotContains(t, payload.String(), `"version":1`, "the basis was serialized from the pre-queue read: the queue wait aged the payload")
	assert.Contains(t, payload.String(), `"state":"s3"`, "the payload must carry the post-admission selector")
}

// TestStreamReplayPostAdmissionReadErrorReleasesSlot pins the error path of the second
// read: a store read that fails after the budget admits the replay must return the slot. A
// leak here would decrease the budget by one for each store error under saturation, exactly
// when a store failure is most probable, until nothing could initialize at all.
func TestStreamReplayPostAdmissionReadErrorReleasesSlot(t *testing.T) {
	const flagKey = "flagkey"
	var reads atomic.Int64
	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		if reads.Add(1) > 1 {
			return nil, subsystems.NoSelector(), errors.New("store went away")
		}
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(1).Build()
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NewSelector("s1", 1), nil
	})

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	hold, ok := limiter.Acquire(context.Background())
	require.True(t, ok)

	repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true, initLimiter: limiter}
	eventCh := repo.ReplayWithContext(context.Background(), "", "s9")
	deadline := time.Now().Add(2 * time.Second)
	for limiter.Stats().Waiting != 1 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "client never queued (stats: %+v)", limiter.Stats())
		}
		runtime.Gosched()
	}
	hold() // admit the client; its refreshed read fails

	select {
	case e, open := <-eventCh:
		assert.False(t, open, "a failed replay must deliver no events (got %v)", e)
	case <-time.After(2 * time.Second):
		require.Fail(t, "replay channel was not closed after the read error")
	}

	// The slot must come back despite the error.
	deadline = time.Now().Add(2 * time.Second)
	for limiter.Stats().Held != 0 {
		if time.Now().After(deadline) {
			require.Failf(t, "timeout", "the slot leaked on the post-admission read error (stats: %+v)", limiter.Stats())
		}
		runtime.Gosched()
	}
	// And the budget still has its full capacity.
	r2, ok2 := limiter.Acquire(context.Background())
	require.True(t, ok2, "budget capacity eroded by the read error")
	r2()
}

func TestStreamReplayCurrentBasisIsUpToDateDespiteConcurrentStaleRead(t *testing.T) {
	const flagKey = "flagkey"
	firstReadStarted := make(chan struct{}, 1)
	releaseFirstRead := make(chan struct{})
	var mu sync.Mutex
	state := 0

	store := newMockStoreQueries()
	store.setupSnapshotFn(func() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector, error) {
		mu.Lock()
		state++
		s := state
		mu.Unlock()
		// Hold only the first read (client A, no basis) in flight so client B subscribes while
		// it is still running. A plain counter is used rather than sync.Once so B's concurrent
		// read is not serialized behind A's. Each caller reads the store as it is when its own
		// read runs, so A sees "s1" and B's separate read sees "s2" -- modeling the store
		// advancing between the two subscriptions.
		if s == 1 {
			firstReadStarted <- struct{}{}
			<-releaseFirstRead
		}
		flag := ldbuilders.NewFlagBuilder(flagKey).Version(s).Build()
		sel := "s" + strconv.Itoa(s)
		return map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{
			ldstoreimpl.Features(): {ldstoretypes.KeyedItemDescriptor{Key: flagKey, Item: sharedtest.FlagDesc(flag)}},
			ldstoreimpl.Segments(): {},
		}, subsystems.NewSelector(sel, s), nil
	})
	repo := &serverSideEnvStreamRepository{store: store, logger: slog.Default(), isV2: true}

	// Client A reconnects with no basis; its read (state s1) blocks in flight.
	chA := repo.ReplayWithContext(context.Background(), "", "")
	<-firstReadStarted

	// Client B reconnects at basis "s2" -- the state its own fresh read will observe.
	chB := repo.ReplayWithContext(context.Background(), "", "s2")

	// B must not have to wait on A's read; it should read fresh and answer up-to-date.
	drain := func(ch <-chan eventsource.Event) []eventsource.Event {
		var out []eventsource.Event
		for {
			e, ok, closed := helpers.TryReceive(ch, 2*time.Second)
			if closed {
				return out
			}
			require.True(t, ok, "timed out waiting for replayed event")
			out = append(out, e)
		}
	}
	eventsB := drain(chB)
	require.Len(t, eventsB, 1, "expected a single up-to-date event, got a full basis (stale-read regression)")
	assert.Equal(t, string(subsystems.EventServerIntent), eventsB[0].Event())
	assert.Contains(t, eventsB[0].Data(), `"intentCode":"none"`)

	// Let A finish so its goroutine is not leaked.
	close(releaseFirstRead)
	drain(chA)
}
