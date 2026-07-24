package streams

import (
	"log/slog"
	"net/http"
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

// ServerTraceFactory produces the eventsource.ServerTrace to attach to a Server for a given stream
// kind and wire protocol ("v1" or "v2"), or nil to leave tracing disabled. It lets the relay package
// wire the OTel bridge into the SSE servers without the streams package depending on the bridge.
type ServerTraceFactory func(streamKind, protocol string) *eventsource.ServerTrace

// Option customizes a StreamProvider created by NewStreamProvider.
type Option func(*providerOptions)

type providerOptions struct {
	basisLimiter   *concurrency.Limiter
	putSendTimeout time.Duration
}

// WithBasisLimiter bounds how many stream replays may send a FULL basis at once,
// drawing from the shared basis-delivery budget (the same limiter polls use).
// Replays that are already up-to-date, and deltas, do not consume the budget. Only
// the server-side stream provider honors this; it is a no-op for other kinds. A nil
// or disabled limiter imposes no limit. putSendTimeout frees a slot if a put cannot
// be delivered to a (disconnected/stuck) client within it.
func WithBasisLimiter(limiter *concurrency.Limiter, putSendTimeout time.Duration) Option {
	return func(o *providerOptions) {
		o.basisLimiter = limiter
		o.putSendTimeout = putSendTimeout
	}
}

// NewStreamProvider creates a StreamProvider implementation for the specified kind of stream endpoint.
// If traceFactory is non-nil, it is used to attach a ServerTrace to each of the provider's two SSE
// servers (fdv1 and fdv2), tagged with the stream kind and protocol. opts customize the provider
// (e.g. WithBasisLimiter for the server-side stream).
func NewStreamProvider(kind basictypes.StreamKind, maxConnTime, pingStreamJitterTime time.Duration, traceFactory ServerTraceFactory, opts ...Option) StreamProvider {
	var o providerOptions
	for _, opt := range opts {
		opt(&o)
	}
	v1Trace, v2Trace := traces(kind, traceFactory)
	switch kind {
	case basictypes.ServerSideFlagsOnlyStream:
		return &serverSideFlagsOnlyStreamProvider{
			fdv1Server: newSSEServer(maxConnTime, v1Trace),
			fdv2Server: newSSEServer(maxConnTime, v2Trace),
		}
	case basictypes.MobilePingStream:
		return &clientSidePingStreamProvider{
			fdv1Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime, v1Trace),
			fdv2Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime, v2Trace),
			isJSClient: false,
		}
	case basictypes.JSClientPingStream:
		return &clientSidePingStreamProvider{
			fdv1Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime, v1Trace),
			fdv2Server: newSSEServerWithJitter(maxConnTime, pingStreamJitterTime, v2Trace),
			isJSClient: true,
		}
	default:
		return &serverSideStreamProvider{
			fdv1Server:     newSSEServer(maxConnTime, v1Trace),
			fdv2Server:     newSSEServer(maxConnTime, v2Trace),
			basisLimiter:   o.basisLimiter,
			putSendTimeout: o.putSendTimeout,
		}
	}
}

// traces builds the fdv1 and fdv2 ServerTraces for a stream kind, or nils if there is no factory.
func traces(kind basictypes.StreamKind, traceFactory ServerTraceFactory) (v1Trace, v2Trace *eventsource.ServerTrace) {
	if traceFactory == nil {
		return nil, nil
	}
	return traceFactory(string(kind), "v1"), traceFactory(string(kind), "v2")
}

func newSSEServer(maxConnTime time.Duration, trace *eventsource.ServerTrace) *eventsource.Server {
	s := eventsource.NewServer()
	s.Gzip = false
	s.AllowCORS = true
	s.ReplayAll = true
	s.MaxConnTime = maxConnTime
	s.Trace = trace
	return s
}

func newSSEServerWithJitter(maxConnTime time.Duration, jitter time.Duration, trace *eventsource.ServerTrace) *eventsource.Server {
	s := eventsource.NewServerWithJitter(jitter)
	s.Gzip = false
	s.AllowCORS = true
	s.ReplayAll = true
	s.MaxConnTime = maxConnTime
	s.Trace = trace
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
