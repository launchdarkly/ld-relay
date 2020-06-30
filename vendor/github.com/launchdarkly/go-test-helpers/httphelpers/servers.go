package httphelpers

import (
	"net/http"
	"net/http/httptest"
)

// WithServer creates an httptest.Server from the given handler, passes the server instance to the given
// function, and ensures that the server is closed afterward.
func WithServer(handler http.Handler, action func(*httptest.Server)) {
	server := httptest.NewServer(handler)
	defer server.Close()
	defer server.CloseClientConnections()
	action(server)
}
