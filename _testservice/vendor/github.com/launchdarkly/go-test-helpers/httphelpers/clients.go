package httphelpers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

type transportFromHandler struct {
	handler http.Handler
}

func (t transportFromHandler) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	defer func() {
		if r := recover(); r != nil {
			if thrownError, ok := r.(error); ok {
				err = thrownError
			} else {
				err = fmt.Errorf("error from handler: %v", r)
			}
			resp = nil
		}
	}()
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, req)
	resp = recorder.Result()
	return
}

// ClientFromHandler returns an http.Client that does not do real network activity, but instead delegates
// to a http.Handler as if that handler were being used by a server.
//
// This makes it possible to reuse the other handler-related functions in this package to control an http.Client
// rather than using the somewhat less convenient RoundTripper interface.
//
// If the handler panics, it returns an error instead of a response. This can be used to simulate an I/O error
// (since the http.Handler interface does not provide any way *not* to return an actual HTTP response).
func ClientFromHandler(handler http.Handler) *http.Client {
	client := *http.DefaultClient
	client.Transport = transportFromHandler{handler}
	return &client
}
