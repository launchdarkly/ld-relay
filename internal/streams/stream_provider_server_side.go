package streams

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// defaultStreamSendTimeout backstops a streaming initialization send when the provider
// was built without an explicit timeout. It bounds only a client that stalls without
// disconnecting; a normal disconnect frees the slot at once via context cancellation.
const defaultStreamSendTimeout = 30 * time.Second

// This is the standard implementation of the /sdk/stream (fdv2) & /all (fdv1)
// stream endpoints for server-side SDKs.
type serverSideStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server

	// initLimiter is the shared initialization-delivery budget (nil or disabled means no
	// limit); sendTimeout backstops a stalled streaming send. Both are threaded to each
	// per-environment repository at Register time.
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
	// stale or absent), never for a cheap up-to-date reply. sendTimeout backstops a
	// stalled send. initLimiter may be nil or disabled, meaning no limit.
	initLimiter *concurrency.Limiter
	sendTimeout time.Duration

	flightGroup singleflight.Group
}

func (s *serverSideStreamProvider) HandlerV1(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.fdv1Server.Handler(credential.String())
}

func (s *serverSideStreamProvider) HandlerV2(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := credential.SDKCredential.(config.SDKKey); !ok {
		return nil
	}

	return func(w http.ResponseWriter, r *http.Request) {
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

		s.fdv2Server.Handler(credential.String()).ServeHTTP(w, r)
	}
}

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
		initLimiter: s.initLimiter, sendTimeout: s.sendTimeout,
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
		initLimiter: s.initLimiter, sendTimeout: s.sendTimeout,
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
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			// The subscriber already disconnected; don't bother building a payload nobody will read.
			r.logger.Info("subscriber disconnected before replay started; skipping replay")
			return
		default:
		}

		// Read the current data set once for this set of concurrent replays. peek holds
		// references to the stored data rather than a serialized copy, so it is cheap; the
		// expensive serialization happens later, under the budget.
		snapshot, selector, err := r.peek()
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

		// This is a full-basis delivery. Draw from the shared budget before serializing,
		// so the budget bounds both the memory of the payload we are about to build and
		// the egress of sending it. The slot is held across the send and released when the
		// send finishes, the client disconnects, or the stall backstop fires.
		if r.initLimiter.Enabled() {
			release, ok := r.initLimiter.Acquire(ctx)
			if !ok {
				// The budget is full, so shed this replay. The connection receives no data,
				// and the SDK times out initialization and reconnects with backoff. The SSE
				// response has already started, so we cannot answer with a 503 here.
				r.logger.Debug("initialization concurrency limit reached; shedding stream replay")
				return
			}
			defer release()
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
// is still charged. A client disconnect (ctx) stops it and frees the slot. The idle timer
// is a backstop for a client that stalls without disconnecting: it fires only if a single
// event cannot be handed off within sendTimeout. It resets on each successful send, so a
// slow but still-progressing client is never abandoned.
func (r *serverSideEnvStreamRepository) sendEvents(ctx context.Context, out chan<- eventsource.Event, events []eventsource.Event) {
	sendTimeout := r.sendTimeout
	if sendTimeout <= 0 {
		sendTimeout = defaultStreamSendTimeout
	}
	idle := time.NewTimer(sendTimeout)
	defer idle.Stop()
	for _, event := range events {
		select {
		case out <- event:
		case <-ctx.Done():
			// The subscriber disconnected before consuming the whole replay; stop producing so
			// this goroutine, its payload, and any held slot are released promptly.
			r.logger.Info("subscriber disconnected mid-replay; stopping replay")
			return
		case <-idle.C:
			return
		}
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(sendTimeout)
	}
}

// peekResult is the single-flight-shared result of reading the store: the data set and
// its selector.
type peekResult struct {
	snapshot map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor
	selector subsystems.Selector
}

// peek reads the current data set and selector, deduplicating concurrent reads with a
// single flight. It uses a fixed key (not the basis) because the store state is the same
// for every caller, so a herd of reconnects at any basis shares one read. The result
// holds references to the stored data rather than a serialized copy, so it is cheap.
func (r *serverSideEnvStreamRepository) peek() (
	map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
	subsystems.Selector,
	error,
) {
	data, err, _ := r.flightGroup.Do("snapshot", func() (interface{}, error) {
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
		return MakeServerSidePutEvent(allData), nil
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
