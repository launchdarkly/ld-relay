package streams

import (
	"net/http"
	"sync"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
)

// This is the standard implementation of a stream for client-side/mobile SDKs that sends only "ping" events,
// and does not do flag evaluations for specific users. The behavior of this stream is that it sends one "ping"
// event on initial connection, and another "ping" every time there is a data update of any kind.

type clientSidePingStreamProvider struct {
	server     *eventsource.Server
	isJSClient bool
	closeOnce  sync.Once
}

type clientSidePingEnvStreamProvider struct {
	server   *eventsource.Server
	channels []string
}

type clientSidePingEnvStreamRepository struct {
	store EnvStoreQueries
}

func (s *clientSidePingStreamProvider) validateCredential(credential config.SDKCredential) string {
	if s.isJSClient {
		if key, ok := credential.(config.EnvironmentID); ok {
			return string(key)
		}
	} else {
		if key, ok := credential.(config.MobileKey); ok {
			return string(key)
		}
	}
	return ""
}

func (s *clientSidePingStreamProvider) Handler(credential config.SDKCredential) http.HandlerFunc {
	if key := s.validateCredential(credential); key != "" {
		return s.server.Handler(key)
	}
	return nil
}

func (s *clientSidePingStreamProvider) Register(
	credential config.SDKCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if key := s.validateCredential(credential); key != "" {
		repo := &clientSidePingEnvStreamRepository{store: store}
		s.server.Register(key, repo)
		envStream := &clientSidePingEnvStreamProvider{server: s.server, channels: []string{key}}
		return envStream
	}
	return nil
}

func (s *clientSidePingStreamProvider) Close() {
	s.closeOnce.Do(func() {
		s.server.Close()
	})
}

func (e *clientSidePingEnvStreamProvider) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	e.server.Publish(e.channels, MakePingEvent())
}

func (e *clientSidePingEnvStreamProvider) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	e.server.Publish(e.channels, MakePingEvent())
}

func (e *clientSidePingEnvStreamProvider) SendHeartbeat() {
	e.server.PublishComment(e.channels, "")
}

func (e *clientSidePingEnvStreamProvider) Close() {
	for _, key := range e.channels {
		e.server.Unregister(key, true)
	}
}

func (r *clientSidePingEnvStreamRepository) Replay(channel, id string) chan eventsource.Event {
	out := make(chan eventsource.Event, 1)
	if r.store.IsInitialized() { // See serverSideEnvStreamRepository.Replay
		out <- MakePingEvent()
	}
	close(out)
	return out
}
