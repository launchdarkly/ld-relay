package core

import (
	"net/http"

	"github.com/gorilla/mux"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/launchdarkly/ld-relay/v6/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/core/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/core/logging"
	"github.com/launchdarkly/ld-relay/v6/core/middleware"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
)

const (
	serverSideStreamLogMessage          = "Application requested server-side /all stream"
	serverSideFlagsOnlyStreamLogMessage = "Application requested server-side /flags stream"
)

// MakeRouter creates and configures a Router containing all of the standard routes for RelayCore. The Relay
// or RelayEnterprise code may add additional routes.
func (r *RelayCore) MakeRouter() *mux.Router {
	router := mux.NewRouter()
	router.Use(logging.GlobalContextLoggersMiddleware(r.Loggers))
	if r.Loggers.GetMinLevel() == ldlog.Debug {
		router.Use(logging.RequestLoggerMiddleware(r.Loggers))
	}
	router.Handle("/status", statusHandler(r)).Methods("GET")

	sdkKeySelector := middleware.SelectEnvironmentByAuthorizationKey(sdks.Server, r)
	mobileKeySelector := middleware.SelectEnvironmentByAuthorizationKey(sdks.Mobile, r)
	jsClientSelector := middleware.SelectEnvironmentByAuthorizationKey(sdks.JSClient, r)

	// Client-side evaluation
	clientSideMiddlewareStack := middleware.Chain(
		middleware.CORS,
		jsClientSelector,
		middleware.RequestCount(metrics.BrowserRequests))

	goalsRouter := router.PathPrefix("/sdk/goals").Subrouter()
	goalsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(goalsRouter))
	goalsRouter.HandleFunc("/{envId}", getGoals).Methods("GET", "OPTIONS")

	clientSideSdkEvalRouter := router.PathPrefix("/sdk/eval/{envId}/").Subrouter()
	clientSideSdkEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideSdkEvalRouter))
	clientSideSdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly(sdks.JSClient)).Methods("GET", "OPTIONS")
	clientSideSdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly(sdks.JSClient)).Methods("REPORT", "OPTIONS")

	clientSideSdkEvalXRouter := router.PathPrefix("/sdk/evalx/{envId}/").Subrouter()
	clientSideSdkEvalXRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideSdkEvalXRouter))
	clientSideSdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags(sdks.JSClient)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(sdks.JSClient)).Methods("REPORT", "OPTIONS")

	serverSideMiddlewareStack := middleware.Chain(
		sdkKeySelector,
		middleware.RequestCount(metrics.ServerRequests))

	serverSideSdkRouter := router.PathPrefix("/sdk/").Subrouter()
	// (?)TODO: there is a bug in gorilla mux (see see https://github.com/gorilla/mux/pull/378) that means the middleware below
	// because it will not be run if it matches any earlier prefix.  Until it is fixed, we have to apply the middleware explicitly
	// serverSideSdkRouter.Use(serverSideMiddlewareStack)

	serverSideEvalRouter := serverSideSdkRouter.PathPrefix("/eval/").Subrouter()
	serverSideEvalRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(sdks.Server)))).Methods("GET")
	serverSideEvalRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(sdks.Server)))).Methods("REPORT")

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(sdks.Server)))).Methods("GET")
	serverSideEvalXRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(sdks.Server)))).Methods("REPORT")

	// PHP SDK endpoints
	serverSideSdkRouter.Handle("/flags", serverSideMiddlewareStack(http.HandlerFunc(pollAllFlagsHandler))).Methods("GET")
	serverSideSdkRouter.Handle("/flags/{key}", serverSideMiddlewareStack(http.HandlerFunc(pollFlagHandler))).Methods("GET")
	serverSideSdkRouter.Handle("/segments/{key}", serverSideMiddlewareStack(http.HandlerFunc(pollSegmentHandler))).Methods("GET")

	// Mobile evaluation
	mobileMiddlewareStack := middleware.Chain(
		mobileKeySelector,
		middleware.RequestCount(metrics.MobileRequests))

	msdkRouter := router.PathPrefix("/msdk/").Subrouter()
	msdkRouter.Use(mobileMiddlewareStack)

	msdkEvalRouter := msdkRouter.PathPrefix("/eval/").Subrouter()
	msdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly(sdks.Mobile)).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly(sdks.Mobile)).Methods("REPORT")

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags(sdks.Mobile)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(sdks.Mobile)).Methods("REPORT")

	mobileStreamRouter := router.PathPrefix("/meval").Subrouter()
	mobileStreamRouter.Use(mobileMiddlewareStack, middleware.Streaming)
	mobilePingWithUser := pingStreamHandlerWithUser(sdks.Mobile, r.mobileStreamProvider)
	mobileStreamRouter.Handle("", middleware.CountMobileConns(mobilePingWithUser)).Methods("REPORT")
	mobileStreamRouter.Handle("/{user}", middleware.CountMobileConns(mobilePingWithUser)).Methods("GET")

	router.Handle("/mping", mobileKeySelector(
		middleware.CountMobileConns(middleware.Streaming(pingStreamHandler(r.mobileStreamProvider))))).Methods("GET")

	jsPing := pingStreamHandler(r.jsClientStreamProvider)
	jsPingWithUser := pingStreamHandlerWithUser(sdks.JSClient, r.jsClientStreamProvider)

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSidePingRouter), middleware.Streaming)
	clientSidePingRouter.Handle("", middleware.CountBrowserConns(jsPing)).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideStreamEvalRouter), middleware.Streaming)
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.Handle("/{user}", middleware.CountBrowserConns(jsPingWithUser)).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.Handle("", middleware.CountBrowserConns(jsPingWithUser)).Methods("REPORT", "OPTIONS")

	mobileEventsRouter := router.PathPrefix("/mobile").Subrouter()
	mobileEventsRouter.Use(mobileMiddlewareStack)
	mobileEventsRouter.Handle("/events/bulk", bulkEventHandler(events.MobileSDKEventsEndpoint)).Methods("POST")
	mobileEventsRouter.Handle("/events", bulkEventHandler(events.MobileSDKEventsEndpoint)).Methods("POST")
	mobileEventsRouter.Handle("", bulkEventHandler(events.MobileSDKEventsEndpoint)).Methods("POST")
	mobileEventsRouter.Handle("/events/diagnostic", bulkEventHandler(events.MobileSDKDiagnosticEventsEndpoint)).Methods("POST")

	clientSideBulkEventsRouter := router.PathPrefix("/events/bulk/{envId}").Subrouter()
	clientSideBulkEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideBulkEventsRouter))
	clientSideBulkEventsRouter.Handle("", bulkEventHandler(events.JavaScriptSDKEventsEndpoint)).Methods("POST", "OPTIONS")

	clientSideDiagnosticEventsRouter := router.PathPrefix("/events/diagnostic/{envId}").Subrouter()
	clientSideDiagnosticEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideBulkEventsRouter))
	clientSideDiagnosticEventsRouter.Handle("", bulkEventHandler(events.JavaScriptSDKDiagnosticEventsEndpoint)).Methods("POST", "OPTIONS")

	clientSideImageEventsRouter := router.PathPrefix("/a/{envId}.gif").Subrouter()
	clientSideImageEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideImageEventsRouter))
	clientSideImageEventsRouter.HandleFunc("", getEventsImage).Methods("GET", "OPTIONS")

	serverSideRouter := router.PathPrefix("").Subrouter()
	serverSideRouter.Use(serverSideMiddlewareStack)
	serverSideRouter.Handle("/bulk", bulkEventHandler(events.ServerSDKEventsEndpoint)).Methods("POST")
	serverSideRouter.Handle("/diagnostic", bulkEventHandler(events.ServerSDKDiagnosticEventsEndpoint)).Methods("POST")
	serverSideRouter.Handle("/all", middleware.CountServerConns(middleware.Streaming(
		streamHandler(r.serverSideStreamProvider, serverSideStreamLogMessage),
	))).Methods("GET")
	serverSideRouter.Handle("/flags", middleware.CountServerConns(middleware.Streaming(
		streamHandler(r.serverSideFlagsStreamProvider, serverSideFlagsOnlyStreamLogMessage),
	))).Methods("GET")

	return router
}
