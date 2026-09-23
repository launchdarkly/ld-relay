package streams

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// StreamProvider is an abstraction of a specific kind of SSE event stream, such as the server-side SDK
// "/all" stream. The streams package provides default implementations of this interface for the streams
// that are supported by the standard Relay Proxy.
//
// Each StreamProvider expects a specific kind of SDKCredential, e.g. config.SDKKey for the server-side
// streams. If the wrong kind of credential is passed, it should behave as it would for an unrecognized
// key. It is important that there can be more than one StreamProvider for a given credential.
type StreamProvider interface {
	// Handler returns an HTTP request handler for the given scoped SDK credential.
	// It can return nil if it does not support this type of credential.
	Handler(credential sdkauth.ScopedCredential) http.HandlerFunc

	// Register tells the StreamProvider about an environment that it should support, and returns an
	// implementation of EnvStreamsUpdates for pushing updates related to that environment. It can
	// return nil if it does not support this type of credential.
	Register(credential sdkauth.ScopedCredential, store EnvStoreQueries, loggers ldlog.Loggers) EnvStreamProvider

	// Close tells the StreamProvider to release all of its resources and close all connections.
	Close()
}

// EnvStreamProvider is an abstraction of publishing events to a stream for a specific environment.
// Implementations of this interface are created by StreamProvider.Register().
type EnvStreamProvider interface {
	EnvStreamUpdates // SendAllDataUpdate, SendSingleItemUpdate

	// SendHeartbeat sends keep-alive data on the stream.
	SendHeartbeat()

	// Close releases all resources for this EnvStreamProvider and closes all connections to it.
	Close()
}

// StreamProviderSettings holds the connection limits applied to every stream endpoint.
type StreamProviderSettings struct {
	// MaxConnTime, if non-zero, closes each stream after this long.
	MaxConnTime time.Duration
	// MaxWriteTime, if non-zero, disconnects a client whose single write takes longer than this.
	MaxWriteTime time.Duration
	// PingStreamJitterTime, if non-zero, jitters ping events on the client-side streams.
	PingStreamJitterTime time.Duration
	// Loggers receives a warning each time MaxWriteTime disconnects a client.
	Loggers ldlog.Loggers
}

// NewStreamProvider creates a StreamProvider implementation for the specified kind of stream endpoint.
func NewStreamProvider(kind basictypes.StreamKind, settings StreamProviderSettings) StreamProvider {
	switch kind {
	case basictypes.ServerSideFlagsOnlyStream:
		return &serverSideFlagsOnlyStreamProvider{
			server: newSSEServer(eventsource.NewServer(), settings),
		}
	case basictypes.MobilePingStream:
		return &clientSidePingStreamProvider{
			server:     newSSEServer(eventsource.NewServerWithJitter(settings.PingStreamJitterTime), settings),
			isJSClient: false,
		}
	case basictypes.JSClientPingStream:
		return &clientSidePingStreamProvider{
			server:     newSSEServer(eventsource.NewServerWithJitter(settings.PingStreamJitterTime), settings),
			isJSClient: true,
		}
	default:
		return &serverSideStreamProvider{
			server: newSSEServer(eventsource.NewServer(), settings),
		}
	}
}

func newSSEServer(s *eventsource.Server, settings StreamProviderSettings) *eventsource.Server {
	s.Gzip = false
	s.AllowCORS = true
	s.ReplayAll = true
	s.MaxConnTime = settings.MaxConnTime
	s.WriteTimeout = settings.MaxWriteTime
	if settings.MaxWriteTime > 0 {
		loggers := settings.Loggers
		s.Trace = &eventsource.ServerTrace{
			// The channel is the SDK credential, so it is never logged.
			WriteError: func(_ context.Context, info eventsource.WriteErrorInfo) {
				if errors.Is(info.Err, os.ErrDeadlineExceeded) {
					loggers.Warnf("Disconnected a stream client that took longer than maxClientWriteTime (%s) to accept a write",
						settings.MaxWriteTime)
				}
			},
		}
	}
	return s
}

func removeDeleted(items []ldstoretypes.KeyedItemDescriptor) []ldstoretypes.KeyedItemDescriptor {
	var ret []ldstoretypes.KeyedItemDescriptor
	for i, keyedItem := range items {
		if keyedItem.Item.Item == nil {
			if ret == nil {
				ret = make([]ldstoretypes.KeyedItemDescriptor, i)
				copy(ret, items[0:i])
			}
		} else {
			if ret != nil {
				ret = append(ret, keyedItem)
			}
		}
	}
	if ret == nil {
		return items
	}
	return ret
}
