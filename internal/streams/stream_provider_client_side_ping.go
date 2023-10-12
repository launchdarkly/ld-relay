package streams

import (
	"net/http"
	"sync"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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

func (s *clientSidePingStreamProvider) validateCredential(credential credential.SDKCredential) bool {
	if s.isJSClient {
		if _, ok := credential.(config.EnvironmentID); ok {
			return true
		}
	} else {
		if _, ok := credential.(config.MobileKey); ok {
			return true
		}
	}
	return false
}

func (s *clientSidePingStreamProvider) Handler(credential sdkauth.ScopedCredential) http.HandlerFunc {
	if !s.validateCredential(credential.SDKCredential) {
		return nil
	}
	return s.server.Handler(credential.String())
}

func (s *clientSidePingStreamProvider) Register(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if !s.validateCredential(credential.SDKCredential) {
		return nil
	}
	repo := &clientSidePingEnvStreamRepository{store: store}
	s.server.Register(credential.String(), repo)
	envStream := &clientSidePingEnvStreamProvider{server: s.server, channels: []string{credential.String()}}
	return envStream
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

func (e *clientSidePingEnvStreamProvider) InvalidateClientSideState() {
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
