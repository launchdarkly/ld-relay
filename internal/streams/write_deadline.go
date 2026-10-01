package streams

import (
	"context"
	"net/http"

	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
)

// withWriteDeadline wraps an SSE handler so that every write carries a write deadline with the
// given unit limits (see the stream shape of initwrite). A client that stops reading has its
// write fail and its connection closed, and the SDK reconnects. With unit nil, the handler is
// returned as it is.
func withWriteDeadline(h http.HandlerFunc, unit *initwrite.Limits) http.HandlerFunc {
	if unit == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		serveWithCut(h, initwrite.WrapStream(w, *unit, initwrite.Limits{}), r.WithContext(ctx), cancel, true)
	}
}

// serveWithCut runs h with the writer iw, and cuts iw when the request context ends. When the
// client goes away in the middle of a write, the write would otherwise continue until its
// deadline, which can be tens of seconds. The cut makes it fail at once, so the memory and the
// egress of the payload end with the connection.
//
// The watcher that cuts iw lives strictly inside this handler: the deferred cancel and wait
// stop it before serveWithCut returns to net/http, and that is what makes its deadline call
// safe. A cut at a normal handler return causes no harm: the connection is ending, and Cut does
// nothing unless a unit is in progress.
//
// If exit is true and the request is HTTP/1, iw also bounds the final flush that net/http does
// after the handler returns (see Writer.Exit). The exit deadline is set before the cancel, so a
// normal return keeps the slack, and a client that already went away gets a deadline of now.
func serveWithCut(h http.Handler, iw *initwrite.Writer, r *http.Request, cancel context.CancelFunc, exit bool) {
	watcherDone := make(chan struct{})
	defer func() { cancel(); <-watcherDone }()
	go func() {
		defer close(watcherDone)
		<-r.Context().Done()
		iw.Cut()
	}()
	if exit && r.ProtoMajor == 1 {
		defer func() {
			if r.Context().Err() != nil {
				iw.Cut()
			}
			iw.Exit()
		}()
	}
	h.ServeHTTP(iw, r)
}
