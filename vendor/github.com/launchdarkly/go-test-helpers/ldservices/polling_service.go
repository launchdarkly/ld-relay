package ldservices

import (
	"net/http"

	"github.com/launchdarkly/go-test-helpers/httphelpers"
)

const serverSideSDKPollingPath = "/sdk/latest-all"

// ServerSidePollingServiceHandler creates an HTTP handler to mimic the LaunchDarkly server-side polling service.
//
// This handler returns JSON data for requests to /sdk/latest-all, and a 404 error for all other requests.
//
// Since this package cannot depend on the LaunchDarkly data model types, the caller is responsible for providing an
// object that can be marshaled to JSON (such as ServerSDKData). If the data parameter is nil, the default response
// is an empty JSON object {}. The data is marshalled again for each request.
//
//     data := NewServerSDKData().Flags(flag1, flag2)
//     handler := PollingServiceHandler(data)
//
// If you want the mock service to return different responses at different points during a test, you can either
// provide a *ServerSDKData and modify its properties, or use a DelegatingHandler or SequentialHandler that can
// be made to delegate to the ServerSidePollingServiceHandler at one time but a different handler at another time.
func ServerSidePollingServiceHandler(data interface{}) http.Handler {
	if data == nil {
		data = map[string]interface{}{} // default is an empty JSON object rather than null
	}
	return httphelpers.HandlerForPath(serverSideSDKPollingPath,
		httphelpers.HandlerForMethod("GET", httphelpers.HandlerWithJSONResponse(data, nil), nil), nil)
}
