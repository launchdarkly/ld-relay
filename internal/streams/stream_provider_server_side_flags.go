package streams

import (
	"net/http"
	"sync"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
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
}

// NewServerSideFlagsOnlyStreamProvider creates a StreamProvider implementation for the server-side
// SDK "/flags" endpoint, which is used only by old SDKs that do not support segments.
func NewServerSideFlagsOnlyStreamProvider(maxConnTime time.Duration) StreamProvider {
	return &serverSideFlagsOnlyStreamProvider{
		server: newSSEServer(maxConnTime),
	}
}

func (s *serverSideFlagsOnlyStreamProvider) Handler(credential config.SDKCredential) http.HandlerFunc {
	if key, ok := credential.(config.SDKKey); ok {
		return s.server.Handler(string(key))
	}
	return nil
}

func (s *serverSideFlagsOnlyStreamProvider) Register(
	credential config.SDKCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if key, ok := credential.(config.SDKKey); ok {
		repo := &serverSideFlagsOnlyEnvStreamRepository{store: store, loggers: loggers}
		s.server.Register(string(key), repo)
		envStream := &serverSideFlagsOnlyEnvStreamProvider{server: s.server, channels: []string{string(key)}}
		return envStream
	}
	return nil
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
	if !r.store.IsInitialized() {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		if r.store.IsInitialized() {
			flags, err := r.store.GetAll(ldstoreimpl.Features())

			if err != nil {
				r.loggers.Errorf("Error getting all flags: %s\n", err.Error())
			} else {
				out <- MakeServerSideFlagsOnlyPutEvent(
					[]ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: flags}})
			}
		}
	}()
	return out
}
