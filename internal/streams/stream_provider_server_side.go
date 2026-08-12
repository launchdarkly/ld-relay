package streams

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// defaultStreamSendTimeout is the per-delivery cap that applies when the provider has no
// configured timeout. The throughput floor (see internal/initwrite) finds a stalled client
// quickly. This cap stops only a client that stays at the floor on a large payload.
const defaultStreamSendTimeout = 2 * time.Minute

// This is the standard implementation of the /sdk/stream (fdv2) & /all (fdv1)
// stream endpoints for server-side SDKs.
type serverSideStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server

	// initLimiter is the shared initialization-delivery budget. A limiter that is nil or
	// disabled applies no limit. Register gives it to each per-environment repository.
	// sendTimeout limits how long one write to a stalled client may block before the relay
	// closes the connection to get the budget slot back (see withInitDeadline).
	initLimiter *concurrency.Limiter
	sendTimeout time.Duration

	closeOnce sync.Once
}

type serverSideEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
	isV2     bool
}

type serverSideEnvStreamRepository struct {
	store  EnvStoreQueries
	logger *slog.Logger
	isV2   bool

	// initLimiter is the shared initialization-delivery budget. A replay draws from it only
	// when it must send a full basis: always for FDv1, and for FDv2 when the basis of the
	// client is old or absent. A cheap up-to-date reply never draws from it. A limiter that
	// is nil or disabled applies no limit.
	initLimiter *concurrency.Limiter

	flightGroup singleflight.Group
}

func (s *serverSideStreamProvider) HandlerV1(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.withInitDeadline(s.fdv1Server.Handler(credential.String()))
}

func (s *serverSideStreamProvider) HandlerV2(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}

	inner := s.fdv2Server.Handler(credential.String())
	return s.withInitDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// While FDv2 supports a basis parameter, the SSE spec and by proxy, the eventsource pkg, does not.
		//
		// To allow us to support the basis feature, we are going to send the basis value as the
		// Last-Event-ID header. This is a standard feature of the SSE spec, and allows us to
		// support the basis feature without having to modify the eventsource package.
		//
		// The Last-Event-ID header is passed back to the repository (defined in
		// this pkg). We will interpret that value as a basis, and respond
		// appropriately.
		if basis := r.URL.Query().Get("basis"); basis != "" {
			r.Header.Set("Last-Event-ID", basis)
		}

		inner.ServeHTTP(w, r)
	}))
}

// closeConnectionKey is the context key under which withInitDeadline stores a function
// that closes the current SSE connection. A shed replay uses it to make the SDK reconnect,
// so the SDK does not stay connected without data.
type closeConnectionKey struct{}

// withInitDeadline wraps an SSE handler. It makes sure a client that holds a budget slot
// cannot keep it without limit. When the init limiter is enabled, the wrapper does two
// things. First, it wraps the response in a progress-aware write deadline (see initwrite):
// a client that reads at the throughput floor or faster keeps its connection, and a client
// that stalls or reads too slowly has its write fail and its connection closed, which frees
// the slot and causes a clean reconnect. Second, it makes the request context cancelable
// and supplies a close function, so a shed replay can close the connection. When the
// limiter is disabled, the wrapper does nothing, and the base behavior applies.
func (s *serverSideStreamProvider) withInitDeadline(h http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.initLimiter.Enabled() {
			h.ServeHTTP(w, r)
			return
		}
		timeout := s.sendTimeout
		if timeout <= 0 {
			timeout = defaultStreamSendTimeout
		}
		iw := initwrite.WrapGated(w, timeout)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		ctx = context.WithValue(ctx, closeConnectionKey{}, func() { cancel() })
		ctx = context.WithValue(ctx, initWriterKey{}, iw)

		// When the client goes away in the middle of a delivery, the producer releases its
		// budget slot at once. But a write blocked on the dead socket would continue until
		// its per-chunk deadline, which can be tens of seconds. This watcher cuts that write
		// when the context ends, so the memory and the egress of the payload end together
		// with the slot. The watcher lives strictly inside this handler: the deferred close
		// below stops it before ServeHTTP returns to net/http, and that is what makes its
		// deadline call safe. A cut at a normal handler return causes no harm: the
		// connection is ending, and Cut does nothing unless a delivery is in progress.
		watcherDone := make(chan struct{})
		defer func() { cancel(); <-watcherDone }()
		go func() {
			defer close(watcherDone)
			<-ctx.Done()
			iw.Cut()
		}()

		h.ServeHTTP(iw, r.WithContext(ctx))
	}
}

// initWriterKey is the context key under which withInitDeadline stores the progress-aware
// writer that wraps the connection. The replay producer, a different goroutine, uses it to
// attach the write deadline to the gated basis through Begin and End.
type initWriterKey struct{}

// testHookSlowBasisClose, when a test sets it, adds a small delay between the close of the
// batch channel and the read of the writer's done signal. The delay makes the end-of-basis
// flush win that race. That is the order in which a read of an old done channel, instead of
// the one captured before the close, would leak the budget slot. The value is always false
// in production. It is an atomic, so a test can set it while producer goroutines read it.
var testHookSlowBasisClose atomic.Bool //nolint:gochecknoglobals // test-only seam; production reads observe the zero value (false)

func (s *serverSideStreamProvider) RegisterV1(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	logger *slog.Logger,
) EnvStreamProvider {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideEnvStreamRepository{
		store: store, logger: logger, isV2: false,
		initLimiter: s.initLimiter,
	}
	s.fdv1Server.Register(credential.String(), repo)
	envStream := &serverSideEnvStreamProvider{server: s.fdv1Server, channels: []string{credential.String()}, isV2: false}
	return envStream
}

func (s *serverSideStreamProvider) RegisterV2(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	logger *slog.Logger,
) EnvStreamProvider {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideEnvStreamRepository{
		store: store, logger: logger, isV2: true,
		initLimiter: s.initLimiter,
	}
	s.fdv2Server.Register(credential.String(), repo)
	envStream := &serverSideEnvStreamProvider{server: s.fdv2Server, channels: []string{credential.String()}, isV2: true}
	return envStream
}

func (s *serverSideStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.fdv1Server.Close()
		s.fdv2Server.Close()
	})
}

func (e *serverSideEnvStreamProvider) Apply(changeSet subsystems.ChangeSet) {
	switch changeSet.IntentCode() {
	case subsystems.IntentTransferFull:
		e.SetBasis(changeSet)
	case subsystems.IntentTransferChanges:
		e.ApplyDelta(changeSet)
	}
}

func (e *serverSideEnvStreamProvider) SetBasis(changeSet subsystems.ChangeSet) {
	if e.isV2 {
		changes, selector := changeSet.Changes(), changeSet.Selector()
		for _, event := range MakeEventsForSetBasis(changes, selector) {
			e.server.Publish(e.channels, event)
		}
	} else {
		allData, err := changeSet.Collections()
		if err != nil {
			return
		}
		e.server.Publish(e.channels, MakeServerSidePutEvent(allData))
	}
}

func (e *serverSideEnvStreamProvider) ApplyDelta(changeSet subsystems.ChangeSet) {
	if e.isV2 {
		changes, selector := changeSet.Changes(), changeSet.Selector()
		for _, event := range MakeEventsForApplyDelta(changes, selector) {
			e.server.Publish(e.channels, event)
		}
	} else {
		allData, err := changeSet.Collections()
		if err != nil {
			return
		}

		for _, collection := range allData {
			for _, item := range collection.Items {
				if item.Item.Item == nil {
					e.server.Publish(e.channels, MakeServerSideDeleteEvent(collection.Kind, item.Key, item.Item.Version))
				} else {
					e.server.Publish(e.channels, MakeServerSidePatchEvent(collection.Kind, item.Key, item.Item))
				}
			}
		}
	}
}

func (e *serverSideEnvStreamProvider) InvalidateClientSideState() {}

func (e *serverSideEnvStreamProvider) SendHeartbeat() {
	e.server.PublishComment(e.channels, "")
}

func (e *serverSideEnvStreamProvider) Close() {
	for _, key := range e.channels {
		e.server.Unregister(key, true)
	}
}

// Ensure the repository advertises context support so the eventsource server calls
// ReplayWithContext (and thus propagates the connection's lifetime) rather than Replay.
var _ eventsource.RepositoryWithContext = (*serverSideEnvStreamRepository)(nil)

// Replay satisfies the eventsource.Repository interface. It delegates to replay with a background
// context; in practice the eventsource server prefers ReplayWithContext (see below) whenever the
// repository implements it, so this context-less path is only a fallback.
func (r *serverSideEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	return r.replay(context.Background(), id)
}

// ReplayWithContext satisfies the eventsource.RepositoryWithContext interface. The eventsource server
// passes the subscribing request's context, which is cancelled when the SDK client disconnects. This
// lets the producer goroutine below stop sending immediately on disconnect instead of blocking on a
// send whose reader has gone away.
func (r *serverSideEnvStreamRepository) ReplayWithContext(ctx context.Context, channel, id string) <-chan eventsource.Event {
	return r.replay(ctx, id)
}

func (r *serverSideEnvStreamRepository) replay(ctx context.Context, id string) chan eventsource.Event {
	out := make(chan eventsource.Event)
	if !r.store.IsInitialized() {
		// If the data store has never been populated, we won't send an initial event. This is desirable
		// behavior because, if Relay is still waiting on flag data from LD, we want SDK clients to stay
		// waiting on Relay; then once Relay gets a "put" event from the LD stream, it will broadcast that
		// event to this stream.
		close(out)
		return out
	}
	// Close the batch channel exactly one time. Usually this occurs at the end of the
	// producer goroutine, but the full-basis path closes it directly, before it waits for
	// the send to finish, so the eventsource handler performs its end-of-batch flush.
	var closeOnce sync.Once
	closeOut := func() { closeOnce.Do(func() { close(out) }) }
	go func() {
		defer closeOut()
		select {
		case <-ctx.Done():
			// The subscriber already disconnected; don't bother building a payload nobody will read.
			r.logger.Info("subscriber disconnected before replay started; skipping replay")
			return
		default:
		}

		// Read the current data set one time for this set of concurrent replays at the same
		// basis. peek holds references to the stored data, not a serialized copy, so it is
		// cheap; the costly serialization occurs later, under the budget. The basis key
		// keeps the up-to-date check below correct (see peek). This first read only decides
		// whether the client needs a full basis at all. The read that supplies the
		// serialization occurs after the budget admits us, so time spent in the queue
		// cannot make the payload old.
		snapshot, selector, err := r.peek(id)
		if err != nil {
			r.logger.Error("error getting all flags", "error", err)
			return
		}

		// An FDv2 client whose basis already agrees with the store gets a small up-to-date
		// reply. That reply builds no payload, so it does not draw from the budget. The
		// basis travels as the Last-Event-ID; see the note in HandlerV2.
		if r.isV2 && id != "" && selector.IsDefined() && selector.State() == id {
			r.sendEvents(ctx, out, MakeEventsForUpToDate(selector))
			return
		}

		// This is a full-basis delivery. Draw from the shared budget before the
		// serialization, so the budget limits how many full bases the relay builds and sends
		// at the same time, and reconnects at the same basis can share one serialization.
		if r.initLimiter.Enabled() {
			release, ok := r.initLimiter.Acquire(ctx)
			if !ok {
				if ctx.Err() != nil {
					// The client disconnected while it waited for a slot. This is the
					// intended exit from the queue: the limiter has already released its
					// place, and the SDK tries again on its own backoff schedule.
					r.logger.Debug("client disconnected while waiting for an initialization slot")
					return
				}
				if r.initLimiter.Closed() {
					// The relay is stopping, and every connection is about to close. The
					// log must not show this as a full budget, and one line for each
					// parked waiter would flood the log at the worst moment.
					r.logger.Debug("relay is shutting down; ending the stream replay")
					return
				}
				// The budget and the queue are full, so shed this replay. The SSE response
				// has already started, so a 503 reply is not possible. Close the connection
				// instead, so the SDK reconnects with backoff and does not stay connected
				// without data.
				r.logger.Warn("initialization concurrency limit reached; closing stream so the SDK reconnects")
				if closeConn, ok := ctx.Value(closeConnectionKey{}).(func()); ok {
					closeConn()
				}
				return
			}

			// The wait for a slot can be long. Read the store again, so the payload is
			// serialized from the store as it is now, and do the up-to-date check again: a
			// client whose basis became current during the wait gets the small reply, and
			// the slot goes back immediately. Without this second read, a same-basis client
			// that joins the serialize flight of this replay could receive a payload as old
			// as the full queue wait of this replay. The stale-join window that remains is
			// the serialization itself, the same as before the limiter existed. (A client
			// that misses a delta in that window becomes correct on its next full-basis
			// transfer, not on the live stream.)
			snapshot, selector, err = r.peek(id)
			if err != nil {
				r.logger.Error("error getting all flags after admission", "error", err)
				release()
				return
			}
			if r.isV2 && id != "" && selector.IsDefined() && selector.State() == id {
				release()
				r.sendEvents(ctx, out, MakeEventsForUpToDate(selector))
				return
			}

			// Keep the slot for the full send, not only until the channel handoff. The
			// eventsource handler writes the events on another goroutine. Begin attaches the
			// write deadline to this basis, and the end-of-batch flush of the handler clears
			// the deadline and signals completion. The deferred closure runs on EVERY exit
			// after Begin, for a completed delivery and for an abandoned one. End marks the
			// delivery as finished; the flush must see that mark, or the deadline would stay
			// after the delivery and later cut a healthy stream. closeOut causes that flush.
			// WaitAndFinish keeps the slot until the basis is flushed or the connection ends
			// -- a disconnect, or a send that the write deadline cut. As a result,
			// MaxConcurrent limits the concurrent sends and the resident payloads, and this
			// includes the single-event FDv1 /all put. Here ctx is the request context,
			// which withInitDeadline derives and WaitAndFinish requires.
			if iw, ok := ctx.Value(initWriterKey{}).(*initwrite.Writer); ok {
				iw.Begin()
				defer func() {
					iw.End()
					closeOut()
					if testHookSlowBasisClose.Load() {
						// Make the end-of-basis flush complete before the wait starts. That is
						// the order in which an early capture of the completion signal, if a
						// regression brought it back, would leak the slot.
						time.Sleep(5 * time.Millisecond)
					}
					iw.WaitAndFinish(ctx)
					release()
				}()
			} else {
				// This path has no progress-aware writer (for example, the Replay path that
				// has no context). Release the slot when the producer returns.
				defer release()
			}
		}

		var events []eventsource.Event
		if r.isV2 {
			events = r.serializeBasisV2(id, snapshot, selector)
		} else {
			events = r.serializePutV1(snapshot)
		}
		r.sendEvents(ctx, out, events)
	}()
	return out
}

// sendEvents streams the events to the handler of the connection while the held slot, if
// there is one, stays charged. A client disconnect (through ctx) stops it and frees the
// slot. This function has no idle timeout, and that is intentional: if the producer stopped
// a partly-sent basis while the connection stays open, a later delta could complete the
// basis into a corrupt data set. The write deadline of the connection limits a client that
// stalls without a disconnect (see withInitDeadline): the deadline closes the connection,
// and the SDK reconnects cleanly.
func (r *serverSideEnvStreamRepository) sendEvents(ctx context.Context, out chan<- eventsource.Event, events []eventsource.Event) {
	for _, event := range events {
		select {
		case out <- event:
		case <-ctx.Done():
			// The subscriber disconnected before it consumed the whole replay. Stop the
			// producer, so this goroutine, its payload, and each held slot are released
			// quickly.
			r.logger.Info("subscriber disconnected mid-replay; stopping replay")
			return
		}
	}
}

// peekResult is the store-read result that a single flight shares: the data set and its
// selector.
type peekResult struct {
	snapshot map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor
	selector subsystems.Selector
}

// peek reads the current data set and selector. It removes duplicate concurrent reads with
// a single flight, and the key of that flight is the basis of the caller. The basis key,
// not a fixed key, is necessary for correctness: callers that share a read also share the
// up-to-date decision made from its selector, and only callers with the same basis come to
// the same decision. With a fixed key, a client that reconnects at the current basis could
// join an older read that started before its basis existed. That client would fail the
// up-to-date check against the old selector, and would receive a full basis at the old
// state -- and lose each delta published between that read and its own subscription. A herd
// of reconnects at the same basis, the usual condition after a restart, continues to share
// one read. The result holds references to the stored data, not a serialized copy, so it
// is cheap.
func (r *serverSideEnvStreamRepository) peek(id string) (
	map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
	subsystems.Selector,
	error,
) {
	data, err, _ := r.flightGroup.Do("snapshot:"+id, func() (interface{}, error) {
		snapshot, selector, err := r.store.Snapshot()
		if err != nil {
			return nil, err
		}
		return peekResult{snapshot: snapshot, selector: selector}, nil
	})
	if err != nil {
		return nil, subsystems.NoSelector(), err
	}
	res := data.(peekResult)
	return res.snapshot, res.selector, nil
}

// serializePutV1 builds the FDv1 "put" event, which contains the full data set, from a
// snapshot that was read before. A single flight removes duplicate work, so concurrent
// reconnects share one serialization.
func (r *serverSideEnvStreamRepository) serializePutV1(
	snapshot map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
) []eventsource.Event {
	data, _, _ := r.flightGroup.Do("getReplayEventV1", func() (interface{}, error) {
		allData := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: removeDeleted(snapshot[ldstoreimpl.Features()])},
			{Kind: ldstoreimpl.Segments(), Items: removeDeleted(snapshot[ldstoreimpl.Segments()])},
		}
		event := MakeServerSidePutEvent(allData)
		// The put event serializes its payload only when something reads it. Cause that
		// serialization here, inside the single flight and while the budget slot is held.
		// Then the slot accounts for the memory of the payload, and everyone in this flight
		// shares it; no connection serializes it again after the slot is released.
		_ = event.Data()
		return event, nil
	})
	// The value is always an eventsource.Event.
	return []eventsource.Event{data.(eventsource.Event)}
}

// serializeBasisV2 builds the FDv2 basis events from a snapshot that was read before. A
// single flight with a basis key removes duplicate work, so concurrent reconnects at the
// same basis share one serialization.
func (r *serverSideEnvStreamRepository) serializeBasisV2(
	basis string,
	snapshot map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
	selector subsystems.Selector,
) []eventsource.Event {
	data, _, _ := r.flightGroup.Do("getReplayEventV2:"+basis, func() (interface{}, error) {
		changes := []subsystems.Change{}
		kinds := map[ldstoretypes.DataKind]subsystems.ObjectKind{
			ldstoreimpl.Features(): subsystems.FlagKind,
			ldstoreimpl.Segments(): subsystems.SegmentKind,
		}
		for dataKind, objectKind := range kinds {
			for _, item := range snapshot[dataKind] {
				// A basis replaces the entire payload, so deleted or missing items are
				// skipped to reduce its size.
				if item.Item.Item == nil {
					continue
				}
				writer := jwriter.NewWriter()
				serializeItem(dataKind, item.Item, &writer)
				changes = append(changes, subsystems.Change{
					Action:  subsystems.ChangeTypePut,
					Kind:    objectKind,
					Key:     item.Key,
					Version: item.Item.Version,
					Object:  writer.Bytes(),
				})
			}
		}
		return MakeEventsForSetBasis(changes, selector), nil
	})
	// The value is always a []eventsource.Event.
	return data.([]eventsource.Event)
}
