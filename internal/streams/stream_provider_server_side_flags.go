package streams

import (
	"net/http"
	"sync"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// This is the standard implementation of the /flags stream for old server-side SDKs.

type serverSideFlagsOnlyStreamProvider struct {
	server    *eventsource.Server
	closeOnce sync.Once
}

type serverSideFlagsOnlyEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
}

type serverSideFlagsOnlyEnvStreamRepository struct {
	store   EnvStoreQueries
	loggers ldlog.Loggers

	flightGroup singleflight.Group
}

func (s *serverSideFlagsOnlyStreamProvider) Handler(params sdkauth.ScopedCredential) http.HandlerFunc {
	if _, ok := params.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	return s.server.Handler(params.String())
}

func (s *serverSideFlagsOnlyStreamProvider) Register(
	params sdkauth.ScopedCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if _, ok := params.SDKCredential.(config.SDKKey); !ok {
		return nil
	}
	repo := &serverSideFlagsOnlyEnvStreamRepository{store: store, loggers: loggers}
	s.server.Register(params.String(), repo)
	envStream := &serverSideFlagsOnlyEnvStreamProvider{server: s.server, channels: []string{params.String()}}
	return envStream
}

func (s *serverSideFlagsOnlyStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.server.Close()
	})
}

func (e *serverSideFlagsOnlyEnvStreamProvider) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	e.server.Publish(e.channels, MakeServerSideFlagsOnlyPutEvent(allData))
}

func (e *serverSideFlagsOnlyEnvStreamProvider) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	if kind != ldstoreimpl.Features() {
		return
	}
	if item.Item == nil {
		e.server.Publish(e.channels, MakeServerSideFlagsOnlyDeleteEvent(key, item.Version))
	} else {
		e.server.Publish(e.channels, MakeServerSideFlagsOnlyPatchEvent(key, item))
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

func (r *serverSideFlagsOnlyEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	out := make(chan eventsource.Event)
	if !r.store.IsInitialized() { // See serverSideEnvStreamRepository.Replay
		close(out)
		return out
	}
	go func() {
		defer close(out)
		event, err := r.getReplayEvent()
		if err == nil && event != nil {
			out <- event
		}
	}()
	return out
}

func (r *serverSideFlagsOnlyEnvStreamRepository) getReplayEvent() (eventsource.Event, error) {
	data, err, _ := r.flightGroup.Do("getReplayEvent", func() (interface{}, error) {
		if !r.store.IsInitialized() {
			return nil, nil
		}
		flags, err := r.store.GetAll(ldstoreimpl.Features())

		if err != nil {
			r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			return nil, err
		}

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
