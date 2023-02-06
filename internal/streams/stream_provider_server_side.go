package streams

import (
	"net/http"
	"sync"

	"github.com/launchdarkly/ld-relay/v7/config"
	"golang.org/x/sync/singleflight"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"
)

// This is the standard implementation of the /all stream for server-side SDKs.

type serverSideStreamProvider struct {
	server    *eventsource.Server
	closeOnce sync.Once
}

type serverSideEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
}

type serverSideEnvStreamRepository struct {
	store   EnvStoreQueries
	loggers ldlog.Loggers

	flightGroup singleflight.Group
}

func (s *serverSideStreamProvider) Handler(credential config.SDKCredential) http.HandlerFunc {
	if key, ok := credential.(config.SDKKey); ok {
		return s.server.Handler(string(key))
	}
	return nil
}

func (s *serverSideStreamProvider) Register(
	credential config.SDKCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if key, ok := credential.(config.SDKKey); ok {
		repo := &serverSideEnvStreamRepository{store: store, loggers: loggers}
		s.server.Register(string(key), repo)
		envStream := &serverSideEnvStreamProvider{server: s.server, channels: []string{string(key)}}
		return envStream
	}
	return nil
}

func (s *serverSideStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.server.Close()
	})
}

func (e *serverSideEnvStreamProvider) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	e.server.Publish(e.channels, MakeServerSidePutEvent(allData))
}

func (e *serverSideEnvStreamProvider) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	if item.Item == nil {
		e.server.Publish(e.channels, MakeServerSideDeleteEvent(kind, key, item.Version))
	} else {
		e.server.Publish(e.channels, MakeServerSidePatchEvent(kind, key, item))
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

func (r *serverSideEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
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
		event, err := r.getReplayEvent()
		if err != nil {
			return
		}
		out <- event
	}()
	return out
}

// getReplayEvent will return a ServerSidePutEvent with all the data needed for a Replay.
func (r *serverSideEnvStreamRepository) getReplayEvent() (eventsource.Event, error) {
	data, err, _ := r.flightGroup.Do("getReplayEvent", func() (interface{}, error) {
		flags, err := r.store.GetAll(ldstoreimpl.Features())

		if err != nil {
			r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			return nil, err
		}
		segments, err := r.store.GetAll(ldstoreimpl.Segments())
		if err != nil {
			r.loggers.Errorf("Error getting all segments: %s\n", err.Error())
			return nil, err
		}

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
	return event, nil
}
