package streams

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// This is the standard implementation of the /flags stream for old server-side SDKs.
type serverSideFlagsOnlyStreamProvider struct {
	fdv1Server *eventsource.Server
	fdv2Server *eventsource.Server
	closeOnce  sync.Once
}

type serverSideFlagsOnlyEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
}

type serverSideFlagsOnlyEnvStreamRepository struct {
	store  EnvStoreQueries
	logger *slog.Logger

	flightGroup singleflight.Group
}

func (s *serverSideFlagsOnlyStreamProvider) HandlerV1(params sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := params.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.fdv1Server.Handler(params.String())
}

func (s *serverSideFlagsOnlyStreamProvider) HandlerV2(params sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := params.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.fdv2Server.Handler(params.String())
}

func (s *serverSideFlagsOnlyStreamProvider) RegisterV1(
	params sdkauth.ScopedCredential,
	store EnvStoreQueries,
	logger *slog.Logger,
) EnvStreamProvider {
	if _, ok := params.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideFlagsOnlyEnvStreamRepository{store: store, logger: logger}
	s.fdv1Server.Register(params.String(), repo)
	envStream := &serverSideFlagsOnlyEnvStreamProvider{server: s.fdv1Server, channels: []string{params.String()}}
	return envStream
}

func (s *serverSideFlagsOnlyStreamProvider) RegisterV2(
	params sdkauth.ScopedCredential,
	store EnvStoreQueries,
	logger *slog.Logger,
) EnvStreamProvider {
	if _, ok := params.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideFlagsOnlyEnvStreamRepository{store: store, logger: logger}
	s.fdv2Server.Register(params.String(), repo)
	envStream := &serverSideFlagsOnlyEnvStreamProvider{server: s.fdv2Server, channels: []string{params.String()}}
	return envStream
}

func (s *serverSideFlagsOnlyStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.fdv1Server.Close()
		s.fdv2Server.Close()
	})
}

func (e *serverSideFlagsOnlyEnvStreamProvider) Apply(changeSet subsystems.ChangeSet) {
	switch changeSet.IntentCode() {
	case subsystems.IntentTransferFull:
		e.setBasis(changeSet)
	case subsystems.IntentTransferChanges:
		e.applyDelta(changeSet)
	}
}

func (e *serverSideFlagsOnlyEnvStreamProvider) setBasis(changeSet subsystems.ChangeSet) {
	allData, err := changeSet.Collections()
	if err != nil {
		return
	}
	e.server.Publish(e.channels, MakeServerSideFlagsOnlyPutEvent(allData))
}

func (e *serverSideFlagsOnlyEnvStreamProvider) applyDelta(changeSet subsystems.ChangeSet) {
	allData, err := changeSet.Collections()
	if err != nil {
		return
	}
	for _, collection := range allData {
		if collection.Kind != ldstoreimpl.Features() {
			continue
		}
		for _, item := range collection.Items {
			if item.Item.Item == nil {
				e.server.Publish(e.channels, MakeServerSideFlagsOnlyDeleteEvent(item.Key, item.Item.Version))
			} else {
				e.server.Publish(e.channels, MakeServerSideFlagsOnlyPatchEvent(item.Key, item.Item))
			}
		}
	}
}

func (e *serverSideFlagsOnlyEnvStreamProvider) InvalidateClientSideState() {}

func (e *serverSideFlagsOnlyEnvStreamProvider) SendHeartbeat() {
	e.server.PublishComment(e.channels, "")
}

func (e *serverSideFlagsOnlyEnvStreamProvider) Close() {
	for _, key := range e.channels {
		e.server.Unregister(key, true)
	}
}

// Ensure the repository advertises context support so the eventsource server calls
// ReplayWithContext (and thus hands over the subscribing request's context) rather than Replay.
var _ eventsource.RepositoryWithContext = (*serverSideFlagsOnlyEnvStreamRepository)(nil)

// Replay satisfies the eventsource.Repository interface. It delegates to replay with a background
// context; in practice the eventsource server prefers ReplayWithContext (below) whenever the
// repository implements it, so this context-less path is only a fallback.
func (r *serverSideFlagsOnlyEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	return r.replay(context.Background())
}

// ReplayWithContext satisfies the eventsource.RepositoryWithContext interface. The context is
// only used for telemetry: the subscribing request's span is annotated with how the replay's
// flight resolved (refer to tracing.SingleflightDo).
func (r *serverSideFlagsOnlyEnvStreamRepository) ReplayWithContext(ctx context.Context, channel, id string) <-chan eventsource.Event {
	return r.replay(ctx)
}

func (r *serverSideFlagsOnlyEnvStreamRepository) replay(ctx context.Context) chan eventsource.Event {
	out := make(chan eventsource.Event)
	if !r.store.IsInitialized() { // See serverSideEnvStreamRepository.Replay
		close(out)
		return out
	}
	go func() {
		defer close(out)
		event, err := r.getReplayEvent(ctx)
		if err == nil && event != nil {
			out <- event
		}
	}()
	return out
}

func (r *serverSideFlagsOnlyEnvStreamRepository) getReplayEvent(ctx context.Context) (eventsource.Event, error) {
	data, err := tracing.SingleflightDo(ctx, &r.flightGroup, "getReplayEvent", func() (interface{}, error) {
		if !r.store.IsInitialized() {
			return nil, nil
		}
		snapshot, _, err := r.store.Snapshot()
		if err != nil {
			r.logger.Error("error getting all flags", "error", err)
			return nil, err
		}
		flags := snapshot[ldstoreimpl.Features()]

		event := MakeServerSideFlagsOnlyPutEvent(
			[]ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: removeDeleted(flags)}})
		return event, nil
	})

	if err != nil {
		return nil, err
	}

	// panic if it's not an eventsource.Event - as this should be impossible
	event := data.(eventsource.Event)
	return event, nil
}
