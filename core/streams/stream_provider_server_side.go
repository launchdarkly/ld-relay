package streams

import (
	"net/http"
	"sync"
	"time"

	config "github.com/launchdarkly/ld-relay/v6/core/config"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
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
}

// NewServerSideStreamProvider creates a StreamProvider implementation for the server-side SDK "/all"
// endpoint, which is used by all current server-side SDK versions.
func NewServerSideStreamProvider(maxConnTime time.Duration) StreamProvider {
	return &serverSideStreamProvider{
		server: newSSEServer(maxConnTime),
	}
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
		close(out)
		return out
	}
	go func() {
		defer close(out)
		flags, err := r.store.GetAll(ldstoreimpl.Features())

		if err != nil {
			r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
		} else {
			segments, err := r.store.GetAll(ldstoreimpl.Segments())
			if err != nil {
				r.loggers.Errorf("Error getting all segments: %s\n", err.Error())
			} else {
				allData := []ldstoretypes.Collection{
					{Kind: ldstoreimpl.Features(), Items: flags},
					{Kind: ldstoreimpl.Segments(), Items: segments},
				}
				out <- MakeServerSidePutEvent(allData)
			}
		}
	}()
	return out
}
