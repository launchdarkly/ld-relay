package streams

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// This is the standard implementation of the /sdk/stream (fdv2) & /all (fdv1)
// stream endpoints for server-side SDKs.
type serverSideStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server
	closeOnce  sync.Once
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
	repo := &serverSideEnvStreamRepository{store: store, logger: logger, isV2: false}
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
	repo := &serverSideEnvStreamRepository{store: store, logger: logger, isV2: true}
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
			return
		default:
		}
		var events []eventsource.Event
		var err error
		if r.isV2 {
			// See the note in HandlerV2 about how we use the Last-Event-ID header to
			// pass the basis.
			events, err = r.getReplayEventsV2(id)
		} else {
			events, err = r.getReplayEventsV1()
		}

		if err != nil {
			return
		}
		for _, event := range events {
			select {
			case out <- event:
			case <-ctx.Done():
				// The subscriber disconnected before consuming the whole replay; stop producing so
				// this goroutine and its payload are released promptly instead of leaking.
				return
			}
		}
	}()
	return out
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

func (r *serverSideEnvStreamRepository) getReplayEventsV2(basis string) ([]eventsource.Event, error) {
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
			return MakeEventsForUpToDate(selector), nil
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

		return MakeEventsForSetBasis(changes, selector), nil
	})

	if err != nil {
		return nil, err
	}

	// panic if it's not an eventsource.Event - as this should be impossible
	return data.([]eventsource.Event), nil
}
