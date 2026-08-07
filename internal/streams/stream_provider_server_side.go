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

// defaultStreamSendTimeout is the absolute per-delivery cap used when the provider was built
// without an explicit timeout. The throughput floor (see internal/initwrite) does the
// fast-stall detection; this only backstops a client stuck at the floor on a large payload.
const defaultStreamSendTimeout = 2 * time.Minute

// This is the standard implementation of the /sdk/stream (fdv2) & /all (fdv1)
// stream endpoints for server-side SDKs.
type serverSideStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server

	// initLimiter is the shared initialization-delivery budget (nil or disabled means no
	// limit); it is threaded to each per-environment repository at Register time.
	// sendTimeout bounds how long a single write to a stalled client may block before the
	// connection is closed to reclaim its budget slot (see withInitDeadline).
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

	// initLimiter is the shared initialization-delivery budget; a replay draws from it
	// only when it must send a full basis (FDv1 always; FDv2 when the client's basis is
	// stale or absent), never for a cheap up-to-date reply. It may be nil or disabled,
	// meaning no limit.
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

// closeConnectionKey is the context key under which withInitDeadline stashes a function
// that closes the current SSE connection. A shed replay reads it to make the SDK reconnect
// instead of sitting connected but uninitialized.
type closeConnectionKey struct{}

// withInitDeadline wraps an SSE handler so a client that holds a budget slot cannot park it
// indefinitely. When the init limiter is enabled it (1) wraps the response in a
// progress-aware write deadline (see initwrite): a client sustaining at least the throughput
// floor keeps its connection, while one that stalls or drops below the floor has its write
// fail and its connection closed, freeing the slot and prompting a clean reconnect; and (2)
// makes the request context cancelable and exposes a close function so a shed replay can
// close the connection. When the limiter is disabled this is a pass-through, preserving base
// behavior.
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
		ctx = context.WithValue(ctx, closeConnectionKey{}, func() { cancel() })
		ctx = context.WithValue(ctx, initWriterKey{}, iw)

		// When the client goes away mid-delivery, the producer releases its budget slot at
		// once, but a write blocked on the dead socket would otherwise drain until its
		// per-chunk deadline (tens of seconds). This watcher cuts that write the moment the
		// context ends, so the payload's memory and egress end with the slot. It is scoped
		// strictly inside this handler: the deferred close below stops it before ServeHTTP
		// returns to net/http, which is what makes its deadline call safe. A cut at normal
		// handler return is harmless: the connection is ending, and Cut does nothing unless
		// a delivery is in flight.
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

// initWriterKey is the context key under which withInitDeadline stashes the progress-aware
// writer wrapping the connection, so the replay producer (a different goroutine) can scope
// the write deadline to the gated basis via Begin/End.
type initWriterKey struct{}

// testHookSlowBasisClose, when set by a test, inserts a small delay between closing the batch
// channel and reading the writer's done signal, forcing the end-of-basis flush to win that
// race -- the interleaving under which reading a stale done channel (rather than the one
// captured before the close) would leak the budget slot. Always false in production; an
// atomic so a test can toggle it while producer goroutines read it.
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
	// close the batch channel exactly once: normally at the end of the producer goroutine, but
	// the full-basis path closes it explicitly (before waiting for the send to finish) so the
	// eventsource handler performs its end-of-batch flush.
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

		// Read the current data set once for this set of concurrent replays at the same basis.
		// peek holds references to the stored data rather than a serialized copy, so it is
		// cheap; the expensive serialization happens later, under the budget. Keying the read
		// by basis keeps the up-to-date check below correct (see peek).
		snapshot, selector, err := r.peek(id)
		if err != nil {
			r.logger.Error("error getting all flags", "error", err)
			return
		}

		// An FDv2 client whose basis already matches the store gets a small up-to-date
		// reply. It builds no payload, so it does not draw from the budget. The basis is
		// carried as the Last-Event-ID; see the note in HandlerV2.
		if r.isV2 && id != "" && selector.IsDefined() && selector.State() == id {
			r.sendEvents(ctx, out, MakeEventsForUpToDate(selector))
			return
		}

		// This is a full-basis delivery. Draw from the shared budget before serializing, so the
		// budget bounds how many full bases are built and sent at once, and lets same-basis
		// reconnects share one serialization.
		if r.initLimiter.Enabled() {
			release, ok := r.initLimiter.Acquire(ctx)
			if !ok {
				if ctx.Err() != nil {
					// The client chose to disconnect while it waited for a slot. This is the
					// designed exit from the queue: the limiter has already relinquished its
					// spot, and the SDK retries on its own backoff schedule.
					r.logger.Debug("client disconnected while waiting for an initialization slot")
					return
				}
				// The budget and queue are full, so shed this replay. The SSE response has
				// already started, so we cannot answer with a 503; instead close the
				// connection so the SDK reconnects with backoff rather than sitting
				// connected but uninitialized.
				r.logger.Warn("initialization concurrency limit reached; closing stream so the SDK reconnects")
				if closeConn, ok := ctx.Value(closeConnectionKey{}).(func()); ok {
					closeConn()
				}
				return
			}

			// Hold the slot for the actual send, not just until the channel handoff. The
			// eventsource handler writes the events on another goroutine; Begin scopes the write
			// deadline to this basis, and the handler's end-of-batch flush clears it and signals
			// completion. The deferred closure runs on EVERY exit after Begin -- a completed
			// delivery and an abandoned one alike: End marks the delivery finished (the flush
			// must observe it, or the armed deadline would outlive the delivery and later cut a
			// healthy stream), closeOut triggers that flush, and WaitAndFinish holds the slot
			// until the basis is flushed or the connection ends (a disconnect, or a send the
			// write deadline cut). This makes MaxConcurrent bound concurrent sends and resident
			// payloads, including the single-event FDv1 /all put. ctx here is the request's
			// context (withInitDeadline derives it), which WaitAndFinish requires.
			if iw, ok := ctx.Value(initWriterKey{}).(*initwrite.Writer); ok {
				iw.Begin()
				defer func() {
					iw.End()
					closeOut()
					if testHookSlowBasisClose.Load() {
						// Force the end-of-basis flush to complete before the wait begins, the
						// ordering under which a regression to eager completion-signal capture
						// would leak the slot.
						time.Sleep(5 * time.Millisecond)
					}
					iw.WaitAndFinish(ctx)
					release()
				}()
			} else {
				// No progress-aware writer on this path (e.g. the context-less Replay fallback);
				// release when the producer returns.
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

// sendEvents streams the events to the connection's handler while the held slot, if any,
// is still charged. A client disconnect (ctx) stops it and frees the slot. There is
// deliberately no idle timeout here: abandoning a partly-sent basis while the connection
// stays open would let a later delta complete it into a corrupt data set. A client that
// stalls without disconnecting is bounded instead by the connection's write deadline (see
// withInitDeadline), which closes the connection so the SDK reconnects cleanly.
func (r *serverSideEnvStreamRepository) sendEvents(ctx context.Context, out chan<- eventsource.Event, events []eventsource.Event) {
	for _, event := range events {
		select {
		case out <- event:
		case <-ctx.Done():
			// The subscriber disconnected before consuming the whole replay; stop producing so
			// this goroutine, its payload, and any held slot are released promptly.
			r.logger.Info("subscriber disconnected mid-replay; stopping replay")
			return
		}
	}
}

// peekResult is the single-flight-shared result of reading the store: the data set and
// its selector.
type peekResult struct {
	snapshot map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor
	selector subsystems.Selector
}

// peek reads the current data set and selector, deduplicating concurrent reads with a
// single flight keyed by the caller's basis. Keying by basis (rather than a fixed key) is
// required for correctness: callers that share a read also share the up-to-date decision
// made from its selector, and only callers with the same basis reach the same decision. A
// fixed key would let a client reconnecting at the current basis join an older in-flight
// read taken before its basis existed, fail the up-to-date check against that stale
// selector, and be sent a full basis at the old state -- losing any delta published between
// that read and its own subscription. A herd of reconnects at the same basis (the common
// case after a restart) still shares one read. The result holds references to the stored
// data rather than a serialized copy, so it is cheap.
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

// serializePutV1 builds the FDv1 "put" event containing the entire data set from an
// already-read snapshot. It is single-flight-deduplicated so concurrent reconnects share
// one serialization.
func (r *serverSideEnvStreamRepository) serializePutV1(
	snapshot map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
) []eventsource.Event {
	data, _, _ := r.flightGroup.Do("getReplayEventV1", func() (interface{}, error) {
		allData := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: removeDeleted(snapshot[ldstoreimpl.Features()])},
			{Kind: ldstoreimpl.Segments(), Items: removeDeleted(snapshot[ldstoreimpl.Segments()])},
		}
		event := MakeServerSidePutEvent(allData)
		// The put event serializes its payload lazily. Force that here, inside the single
		// flight and while the budget slot is held, so the payload's memory is accounted
		// under the slot and shared by everyone in this flight rather than re-serialized
		// per connection after the slot is released.
		_ = event.Data()
		return event, nil
	})
	// The value is always an eventsource.Event.
	return []eventsource.Event{data.(eventsource.Event)}
}

// serializeBasisV2 builds the FDv2 basis events from an already-read snapshot. It is
// single-flight-deduplicated by basis, so concurrent reconnects at the same basis share
// one serialization.
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
