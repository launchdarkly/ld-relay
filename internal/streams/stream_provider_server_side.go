package streams

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/launchdarkly/go-jsonstream/v4/jwriter"
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
// was built without an explicit timeout. It bounds only a stalled (non-progressing,
// non-disconnecting) client; normal disconnects free the slot via context cancellation.
const defaultStreamSendTimeout = 30 * time.Second

// This is the standard implementation of the /sdk/stream (fdv2) & /all (fdv1)
// stream endpoints for server-side SDKs.
type serverSideStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server

	// initLimiter is the shared initialization-delivery budget (nil/disabled = no
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
	// only when it must send a FULL basis (FDv1 always; FDv2 cold/stale), never when it
	// is up-to-date. envKey scopes per-environment fairness. sendTimeout backstops a
	// stalled send. initLimiter may be nil/disabled (no limit).
	initLimiter *concurrency.Limiter
	envKey      string
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
		initLimiter: s.initLimiter, envKey: credential.String(), sendTimeout: s.sendTimeout,
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
		initLimiter: s.initLimiter, envKey: credential.String(), sendTimeout: s.sendTimeout,
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

// Replay implements eventsource.Repository. It delegates to replay with a background
// context; an eventsource Server that supports RepositoryWithContext calls
// ReplayWithContext instead, which supplies the subscriber's request context.
func (r *serverSideEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	return r.replay(context.Background(), id)
}

// ReplayWithContext implements eventsource.RepositoryWithContext. The eventsource Server
// passes the subscribing request's context, which is cancelled when the client
// disconnects -- so an initialization delivery that is holding a shared slot across its
// send can abandon the send and free the slot the instant the client goes away.
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

		// Generate the payload first. Generation is single-flight-deduplicated by basis,
		// so a herd of identical reconnects shares ONE serialization; it is bounded by the
		// number of distinct in-flight bases and is transient. It is also where we learn
		// whether this replay is a full basis or a cheap up-to-date reply -- which we
		// cannot know without inspecting the store -- so it necessarily runs before we can
		// decide whether to draw from the budget.
		var events []eventsource.Event
		var fullBasis bool
		var err error
		if r.isV2 {
			// See the note in HandlerV2 about how we use the Last-Event-ID header to
			// pass the basis.
			events, fullBasis, err = r.getReplayEventsV2(id)
		} else {
			events, err = r.getReplayEventsV1()
			fullBasis = true // an FDv1 replay is always a full put
		}
		if err != nil {
			return
		}

		// Only a full-basis delivery draws from the shared initialization budget.
		// Up-to-date replies and deltas are cheap and must never be gated or shed. The
		// slot is held across the SEND below -- the resident payload buffer and the egress
		// share are exactly what we are metering -- and released (via defer) when the send
		// finishes, the client disconnects (ctx), or the stall backstop fires.
		if fullBasis && r.initLimiter.Enabled() {
			release, ok := r.initLimiter.Acquire(ctx, r.envKey)
			if !ok {
				// Budget saturated: shed this replay. The SSE 200/preamble has already been
				// written, so we cannot answer 503 here; the connection simply receives no
				// data and the SDK times out initialization and reconnects with backoff.
				r.logger.Debug("initialization concurrency limit reached; shedding stream replay", "env", r.envKey)
				return
			}
			defer release()
		}

		r.sendEvents(ctx, out, events)
	}()
	return out
}

// sendEvents streams the generated events to the connection's handler goroutine while a
// held slot (if any) is still charged. Client disconnect (ctx) stops it at once and frees
// the slot; the idle timer is a backstop that fires only if a single event cannot be
// handed off within sendTimeout -- i.e. a client that has stalled without disconnecting.
// The timer resets on each successful send, so a slow but progressing client is never
// abandoned.
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

// getReplayEvent will return a ServerSidePutEvent with all the data needed for a Replay.
func (r *serverSideEnvStreamRepository) getReplayEventsV1() ([]eventsource.Event, error) {
	data, err, _ := r.flightGroup.Do("getReplayEventV1", func() (interface{}, error) {
		snapshot, _, err := r.store.Snapshot()
		if err != nil {
			r.logger.Error("error getting all flags", "error", err)
			return nil, err
		}

		flags := snapshot[ldstoreimpl.Features()]
		segments := snapshot[ldstoreimpl.Segments()]

		allData := []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: removeDeleted(flags)},
			{Kind: ldstoreimpl.Segments(), Items: removeDeleted(segments)},
		}

		event := MakeServerSidePutEvent(allData)
		return event, nil
	})

	if err != nil {
		return nil, err
	}

	// panic if it's not an eventsource.Event - as this should be impossible
	event := data.(eventsource.Event)
	return []eventsource.Event{event}, nil
}

// v2ReplayResult is the single-flight-shared result of an FDv2 replay: the events to
// send, and whether they constitute a full basis (heavy) rather than an up-to-date reply
// (cheap). fullBasis drives whether the send draws from the initialization budget.
type v2ReplayResult struct {
	events    []eventsource.Event
	fullBasis bool
}

func (r *serverSideEnvStreamRepository) getReplayEventsV2(basis string) ([]eventsource.Event, bool, error) {
	// The result depends on the caller's basis: a client whose basis matches the current
	// selector state gets an "up-to-date" event, while any other client gets a full data
	// transfer. Only requests with the same basis may share a result, so the basis must be
	// part of the key.
	data, err, _ := r.flightGroup.Do("getReplayEventV2:"+basis, func() (interface{}, error) {
		snapshot, selector, err := r.store.Snapshot()
		if err != nil {
			r.logger.Error("error getting all flags", "error", err)
			return nil, err
		}

		if basis != "" && selector.IsDefined() && selector.State() == basis {
			return v2ReplayResult{events: MakeEventsForUpToDate(selector), fullBasis: false}, nil
		}

		changes := []subsystems.Change{}
		kinds := map[ldstoretypes.DataKind]subsystems.ObjectKind{
			ldstoreimpl.Features(): subsystems.FlagKind,
			ldstoreimpl.Segments(): subsystems.SegmentKind,
		}
		for dataKind, objectKind := range kinds {
			items, ok := snapshot[dataKind]
			if ok {
				for _, item := range items {
					// This replay event is always replacing the entire payload, so we
					// can ignore deleted / missing items to reduce the payload size.
					if item.Item.Item == nil {
						continue
					}

					writer := jwriter.NewWriter()
					serializeItem(dataKind, item.Item, &writer)
					json := writer.Bytes()
					changes = append(changes, subsystems.Change{
						Action:  subsystems.ChangeTypePut,
						Kind:    objectKind,
						Key:     item.Key,
						Version: item.Item.Version,
						Object:  json,
					})
				}
			}
		}

		return v2ReplayResult{events: MakeEventsForSetBasis(changes, selector), fullBasis: true}, nil
	})

	if err != nil {
		return nil, false, err
	}

	// panic if it's not a v2ReplayResult - as this should be impossible
	res := data.(v2ReplayResult)
	return res.events, res.fullBasis, nil
}
