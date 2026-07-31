package streams

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"

	"github.com/launchdarkly/eventsource"
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
	// HandlerV1 returns an HTTP request handler for the given scoped SDK credential.
	// It can return nil if it does not support this type of credential.
	//
	// This handler will service requests using the old FDv1 protocol.
	HandlerV1(credential sdkauth.ScopedCredential) http.HandlerFunc

	// HandlerV2 returns an HTTP request handler for the given scoped SDK credential.
	// It can return nil if it does not support this type of credential.
	//
	// This handler will service requests using the new FDv2 protocol.
	HandlerV2(credential sdkauth.ScopedCredential) http.HandlerFunc

	// RegisterV1 tells the StreamProvider about an environment that it should support, and returns an
	// implementation of EnvStreamProvider for pushing updates related to that environment. It can
	// return nil if it does not support this type of credential.
	//
	// This method is used for the old FDv1 protocol.
	RegisterV1(credential sdkauth.ScopedCredential, store EnvStoreQueries, logger *slog.Logger) EnvStreamProvider

	// RegisterV2 tells the StreamProvider about an environment that it should support, and returns an
	// implementation of EnvStreamProvider for pushing updates related to that environment. It can
	// return nil if it does not support this type of credential.
	//
	// This method is used for the old FDv2 protocol.
	RegisterV2(credential sdkauth.ScopedCredential, store EnvStoreQueries, logger *slog.Logger) EnvStreamProvider

	// Close tells the StreamProvider to release all of its resources and close all connections.
	Close()
}

// EnvStreamProvider is an abstraction of publishing events to a stream for a specific environment.
// Implementations of this interface are created by StreamProvider.Register().
type EnvStreamProvider interface {
	EnvStreamUpdates // Apply

	// SendHeartbeat sends keep-alive data on the stream.
	SendHeartbeat()

	// Close releases all resources for this EnvStreamProvider and closes all connections to it.
	Close()
}

// Option customizes a StreamProvider created by NewStreamProvider.
type Option func(*providerOptions)

type providerOptions struct {
	initLimiter *concurrency.Limiter
	sendTimeout time.Duration
	logger      *slog.Logger
}

// WithLogger routes the underlying SSE server's connection-write errors to a logger, so that
// a connection the init limiter cut (its write deadline exceeded) is visible rather than
// silently dropped. Only the server-side stream provider uses it. Without it, those errors go
// nowhere.
func WithLogger(logger *slog.Logger) Option {
	return func(o *providerOptions) {
		o.logger = logger
	}
}

// WithInitLimiter bounds how many stream replays may send a FULL basis at once, drawing
// from the shared initialization-delivery budget (the same limiter polls use). Replays
// that are already up-to-date, and deltas, do not draw from it. Only the server-side
// stream provider honors this; it is a no-op for the other stream kinds. A nil or
// disabled limiter imposes no limit. sendTimeout frees a slot if an initialization
// payload makes no progress reaching a (stalled) client within it.
func WithInitLimiter(limiter *concurrency.Limiter, sendTimeout time.Duration) Option {
	return func(o *providerOptions) {
		o.initLimiter = limiter
		o.sendTimeout = sendTimeout
	}
}

// NewStreamProvider creates a StreamProvider implementation for the specified kind of stream endpoint.
// opts customize the provider (e.g. WithInitLimiter for the server-side stream); they are ignored by
// the stream kinds that do not deliver a full basis.
func NewStreamProvider(kind basictypes.StreamKind, maxConnTime, pingStreamJitterTime time.Duration, opts ...Option) StreamProvider {
	var o providerOptions
	for _, opt := range opts {
		opt(&o)
	}
	switch kind {
	case basictypes.ServerSideFlagsOnlyStream:
		return &serverSideFlagsOnlyStreamProvider{
			fdv1Server: newSSEServer(maxConnTime),
			fdv2Server: newSSEServer(maxConnTime),
		}
	case basictypes.MobilePingStream:
		return &clientSidePingStreamProvider{
			fdv1Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime),
			fdv2Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime),
			isJSClient: false,
		}
	case basictypes.JSClientPingStream:
		return &clientSidePingStreamProvider{
			fdv1Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime),
			fdv2Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime),
			isJSClient: true,
		}
	default:
		fdv1, fdv2 := newSSEServer(maxConnTime), newSSEServer(maxConnTime)
		if o.logger != nil {
			l := sseLogger{log: o.logger}
			fdv1.Logger = l
			fdv2.Logger = l
		}
		return &serverSideStreamProvider{
			fdv1Server:  fdv1,
			fdv2Server:  fdv2,
			initLimiter: o.initLimiter,
			sendTimeout: o.sendTimeout,
		}
	}
}

// sseLogger adapts a slog.Logger to the eventsource.Logger interface. The eventsource server
// uses it only to report a connection-write failure. A write the init limiter's deadline cut
// surfaces as a deadline-exceeded error and is logged at warn (a relay-initiated close, worth
// seeing); an ordinary client disconnect is logged at debug so it does not spam the logs.
type sseLogger struct{ log *slog.Logger }

func (l sseLogger) Println(v ...interface{}) {
	for _, a := range v {
		if err, ok := a.(error); ok && errors.Is(err, os.ErrDeadlineExceeded) {
			l.log.Warn("stream write deadline exceeded; connection closed to reclaim the initialization-delivery slot", "error", err)
			return
		}
	}
	l.log.Debug("stream connection write ended", "detail", fmt.Sprint(v...))
}

func (l sseLogger) Printf(format string, v ...interface{}) {
	l.log.Debug(fmt.Sprintf(format, v...))
}

func newSSEServer(maxConnTime time.Duration) *eventsource.Server {
	s := eventsource.NewServer()
	s.Gzip = false
	s.AllowCORS = true
	s.ReplayAll = true
	s.MaxConnTime = maxConnTime
	return s
}

func newSSEServerWithJitter(maxConnTime time.Duration, jitter time.Duration) *eventsource.Server {
	s := eventsource.NewServerWithJitter(jitter)
	s.Gzip = false
	s.AllowCORS = true
	s.ReplayAll = true
	s.MaxConnTime = maxConnTime
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
