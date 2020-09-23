package ldservices

import (
	"net/http"

	"github.com/launchdarkly/go-test-helpers/httphelpers"
)

const (
	serverSideEventsPath           = "/bulk"
	serverSideDiagnosticEventsPath = "/diagnostic"
)

// ServerSideEventsServiceHandler creates an HTTP handler to mimic the LaunchDarkly server-side events service.
// It returns a 202 status for POSTs to the /bulk and /diagnostic paths, otherwise a 404.
func ServerSideEventsServiceHandler() http.Handler {
	return httphelpers.HandlerForPath(
		serverSideEventsPath,
		httphelpers.HandlerForMethod("POST", httphelpers.HandlerWithStatus(202), nil),
		httphelpers.HandlerForPath(
			serverSideDiagnosticEventsPath,
			httphelpers.HandlerForMethod("POST", httphelpers.HandlerWithStatus(202), nil),
			nil,
		),
	)
}
