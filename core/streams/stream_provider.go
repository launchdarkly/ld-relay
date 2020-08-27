package streams

import (
	"net/http"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// StreamProvider is an abstraction of a specific kind of SSE event stream, such as the server-side SDK
// "/all" stream. The streams package provides default implementations of this interface for the streams
// that are supported by the standard Relay Proxy.
//
// Each StreamProvider expects a specific kind of SDKCredential, e.g. config.SDKKey for the server-side
// streams. If the wrong kind of credential is passed, it should behave as it would for an unrecognized
// key. It is important that there can be more than one StreamProvider for a given credential.
type StreamProvider interface {
	// Handler returns an HTTP request handler for the given credential. It can return nil if it does
	// not support this type of credential.
	Handler(credential config.SDKCredential) http.HandlerFunc

	// Register tells the StreamProvider about an environment that it should support, and returns an
	// implementation of EnvStreamsUpdates for pushing updates related to that environment. It can
	// return nil if it does not support this type of credential.
	Register(credential config.SDKCredential, store EnvStoreQueries, loggers ldlog.Loggers) EnvStreamProvider

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

func newSSEServer(maxConnTime time.Duration) *eventsource.Server {
	s := eventsource.NewServer()
	s.Gzip = false
	s.AllowCORS = true
	s.ReplayAll = true
	s.MaxConnTime = maxConnTime
	return s
}
