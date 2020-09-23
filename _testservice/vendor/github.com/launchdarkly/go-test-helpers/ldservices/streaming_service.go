package ldservices

import (
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-test-helpers/httphelpers"
)

const (
	// ServerSideSDKStreamingPath is the expected request path for server-side SDK stream requests.
	ServerSideSDKStreamingPath = "/all"
	// ClientSideSDKStreamingBasePath is the expected request path for client-side SDK stream requests that
	// use the REPORT method, or the base path (not including ClientSideOrMobileUserSubpath) for requests that
	// use the GET method.
	ClientSideSDKStreamingBasePath = "/eval"
	// MobileSDKStreamingBasePath is the expected request path for mobile SDK stream requests that
	// use the REPORT method, or the base path (not including ClientSideOrMobileUserSubpath) for requests that
	// use the GET method.
	MobileSDKStreamingBasePath  = "/meval"
	arbitraryPathComponentRegex = "/[^/]*"
)

// StreamingServiceHandler creates an HTTP handler that provides an SSE stream.
//
// This is a very simplistic implementation, not suitable for use in a real server that must handle multiple clients
// simultaneously (it has a single channel for events, so if there are multiple requests the events will be divided
// up random between them). Real applications should instead use the Server type in eventsource, which provides a
// multiplexed publish-subscribe model.
//
// If initialEvent is non-nil, it will be sent at the beginning of each connection; you can pass a *ServerSDKData value
// to generate a "put" event.
//
// Any events that are pushed to eventsCh (if it is non-nil) will be published to the stream.
//
// Calling Close() on the returned io.Closer causes the handler to close any active stream connections and refuse all
// subsequent requests. You don't need to do this unless you need to force a stream to disconnect before the test
// server has been shut down; shutting down the server will close connections anyway.
func StreamingServiceHandler(
	initialEvent eventsource.Event,
	eventsCh <-chan eventsource.Event,
) (http.Handler, io.Closer) {
	closerCh := make(chan struct{})
	sh := &streamingServiceHandler{
		initialEvent: initialEvent,
		eventsCh:     eventsCh,
		closerCh:     closerCh,
	}
	c := &streamingServiceCloser{
		closerCh: closerCh,
	}
	return sh, c
}

// ServerSideStreamingServiceHandler creates an HTTP handler to mimic the LaunchDarkly server-side streaming service.
//
// This is the same as StreamingServiceHandler, but enforces that the request path is ServerSideSDKStreamingPath and
// that the method is GET.
//
//     initialData := ldservices.NewServerSDKData().Flags(flag1, flag2) // all clients will get this in a "put" event
//     eventsCh := make(chan eventsource.Event)
//     handler, closer := ldservices.ServerSideStreamingHandler(initialData, eventsCh)
//     server := httptest.NewServer(handler)
//     eventsCh <- ldservices.NewSSEEvent("", "patch", myPatchData) // push a "patch" event
//     closer.Close() // force any current stream connections to be closed
func ServerSideStreamingServiceHandler(
	initialEvent eventsource.Event,
	eventsCh <-chan eventsource.Event,
) (http.Handler, io.Closer) {
	handler, closer := StreamingServiceHandler(initialEvent, eventsCh)
	return httphelpers.HandlerForPath(ServerSideSDKStreamingPath, httphelpers.HandlerForMethod("GET", handler, nil), nil),
		closer
}

// ClientSideStreamingServiceHandler creates an HTTP handler to mimic the LaunchDarkly client-side streaming service.
//
// This is the same as StreamingServiceHandler, but enforces that the request path and method correspond to one of
// the client-side/mobile endpoints.
//
//     initialData := ldservices.NewClientSDKData().Flags(flag1, flag2) // all clients will get this in a "put" event
//     eventsCh := make(chan eventsource.Event)
//     handler, closer := ldservices.ClientSideStreamingHandler(initialData, eventsCh)
//     server := httptest.NewServer(handler)
//     eventsCh <- myUpdatedFlag // push a "patch" event
//     closer.Close() // force any current stream connections to be closed
func ClientSideStreamingServiceHandler(
	initialEvent eventsource.Event,
	eventsCh <-chan eventsource.Event,
) (http.Handler, io.Closer) {
	handler, closer := StreamingServiceHandler(initialEvent, eventsCh)
	return httphelpers.HandlerForPathRegex(
		"^"+ClientSideSDKStreamingBasePath+arbitraryPathComponentRegex+"$",
		httphelpers.HandlerForMethod("REPORT", handler, nil),
		httphelpers.HandlerForPathRegex(
			"^"+ClientSideSDKStreamingBasePath+arbitraryPathComponentRegex+arbitraryPathComponentRegex+"$",
			httphelpers.HandlerForMethod("GET", handler, nil),
			httphelpers.HandlerForPath(
				MobileSDKStreamingBasePath,
				httphelpers.HandlerForMethod("REPORT", handler, nil),
				httphelpers.HandlerForPathRegex(
					"^"+MobileSDKStreamingBasePath+arbitraryPathComponentRegex+"$",
					httphelpers.HandlerForMethod("GET", handler, nil),
					nil,
				),
			),
		),
	), closer
}

type streamingServiceHandler struct {
	initialEvent eventsource.Event
	eventsCh     <-chan eventsource.Event
	closed       bool
	closerCh     <-chan struct{}
}

type streamingServiceCloser struct {
	closerCh  chan<- struct{}
	closeOnce sync.Once
}

func (s *streamingServiceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("StreamingServiceHandler can't be used with a ResponseWriter that does not support Flush")
		w.WriteHeader(500)
		return
	}
	if s.closed {
		log.Println("StreamingServiceHandler received a request after it was closed")
		w.WriteHeader(500)
		return
	}

	// Note that we're not using eventsource.Server to provide a streamed response, because eventsource doesn't
	// have a mechanism for forcing the server to drop the connection while the client is still waiting, and
	// that's a condition we want to be able to simulate in tests.

	var closeNotifyCh <-chan bool
	// CloseNotifier is deprecated but there's no way to use Context in this case
	if closeNotifier, ok := w.(http.CloseNotifier); ok { //nolint:megacheck
		closeNotifyCh = closeNotifier.CloseNotify()
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")

	encoder := eventsource.NewEncoder(w, false)

	if s.initialEvent != nil {
		_ = encoder.Encode(s.initialEvent)
	}
	flusher.Flush()

StreamLoop:
	for {
		select {
		case e := <-s.eventsCh:
			_ = encoder.Encode(e)
			flusher.Flush()
		case <-s.closerCh:
			s.closed = true
			break StreamLoop
		case <-closeNotifyCh:
			// client has closed the connection
			break StreamLoop
		}
	}
}

func (c *streamingServiceCloser) Close() error {
	c.closeOnce.Do(func() {
		close(c.closerCh)
	})
	return nil
}
