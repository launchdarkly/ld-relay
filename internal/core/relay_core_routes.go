package core

import (
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/core/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/core/middleware"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"

	"github.com/gorilla/mux"
)

const (
	serverSideStreamLogMessage          = "Application requested server-side /all stream"
	serverSideFlagsOnlyStreamLogMessage = "Application requested server-side /flags stream"
)

// MakeRouter creates and configures a Router containing all of the standard routes for RelayCore. The Relay
// or RelayEnterprise code may add additional routes.
//
// IMPORTANT: The route strings that are used here, such as "/sdk/evalx/{envId}/users/{user}", will appear
// in metrics data under the "route" tag if Relay is configured to export metrics. Therefore, we should use
// variable names like {envId} consistently and make sure they correspond to how the routes are shown in
// docs/endpoints.md.
func (r *RelayCore) MakeRouter() *mux.Router {
	router := mux.NewRouter()
	router.Use(logging.GlobalContextLoggersMiddleware(r.Loggers))
	if r.Loggers.GetMinLevel() == ldlog.Debug {
		router.Use(logging.RequestLoggerMiddleware(r.Loggers))
	}
	router.Handle("/status", statusHandler(r)).Methods("GET")

	sdkKeySelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, r)
	mobileKeySelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, r)
	jsClientSelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, r)
	offlineMode := r.config.OfflineMode.FileDataSource != ""

	// Client-side evaluation (for JS, not mobile)
	jsClientSideMiddlewareStack := func(subrouter *mux.Router) mux.MiddlewareFunc {
		return middleware.Chain(
			mux.CORSMethodMiddleware(subrouter),
			middleware.CORS,
			jsClientSelector,
			middleware.RequestCount(metrics.BrowserRequests),
		)
	}

	goalsRouter := router.PathPrefix("/sdk/goals").Subrouter()
	goalsRouter.Use(jsClientSideMiddlewareStack(goalsRouter))
	goalsRouter.HandleFunc("/{envId}", getGoals).Methods("GET", "OPTIONS")

	clientSideSdkEvalRouter := router.PathPrefix("/sdk/eval/{envId}/").Subrouter()
	clientSideSdkEvalRouter.Use(jsClientSideMiddlewareStack(clientSideSdkEvalRouter))
	clientSideSdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly(basictypes.JSClientSDK)).Methods("GET", "OPTIONS")
	clientSideSdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly(basictypes.JSClientSDK)).Methods("REPORT", "OPTIONS")

	clientSideSdkEvalXRouter := router.PathPrefix("/sdk/evalx/{envId}/").Subrouter()
	clientSideSdkEvalXRouter.Use(jsClientSideMiddlewareStack(clientSideSdkEvalXRouter))
	clientSideSdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags(basictypes.JSClientSDK)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(basictypes.JSClientSDK)).Methods("REPORT", "OPTIONS")

	serverSideMiddlewareStack := middleware.Chain(
		sdkKeySelector,
		middleware.RequestCount(metrics.ServerRequests))

	serverSideSdkRouter := router.PathPrefix("/sdk/").Subrouter()
	// (?)TODO: there is a bug in gorilla mux (see see https://github.com/gorilla/mux/pull/378) that means the middleware below
	// because it will not be run if it matches any earlier prefix.  Until it is fixed, we have to apply the middleware explicitly
	// serverSideSdkRouter.Use(serverSideMiddlewareStack)

	serverSideEvalRouter := serverSideSdkRouter.PathPrefix("/eval/").Subrouter()
	serverSideEvalRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(basictypes.ServerSDK)))).Methods("GET")
	serverSideEvalRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(basictypes.ServerSDK)))).Methods("REPORT")

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK)))).Methods("GET")
	serverSideEvalXRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK)))).Methods("REPORT")

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
	msdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly(basictypes.MobileSDK)).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly(basictypes.MobileSDK)).Methods("REPORT")

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags(basictypes.MobileSDK)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(basictypes.MobileSDK)).Methods("REPORT")

	mobileStreamRouter := router.PathPrefix("/meval").Subrouter()
	mobileStreamRouter.Use(mobileMiddlewareStack, middleware.Streaming)
	mobilePingWithUser := pingStreamHandlerWithUser(basictypes.MobileSDK, r.mobileStreamProvider)
	mobileStreamRouter.Handle("", middleware.CountMobileConns(mobilePingWithUser)).Methods("REPORT")
	mobileStreamRouter.Handle("/{user}", middleware.CountMobileConns(mobilePingWithUser)).Methods("GET")

	router.Handle("/mping", mobileKeySelector(
		middleware.CountMobileConns(middleware.Streaming(pingStreamHandler(r.mobileStreamProvider))))).Methods("GET")

	jsPing := pingStreamHandler(r.jsClientStreamProvider)
	jsPingWithUser := pingStreamHandlerWithUser(basictypes.JSClientSDK, r.jsClientStreamProvider)

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(jsClientSideMiddlewareStack(clientSidePingRouter), middleware.Streaming)
	clientSidePingRouter.Handle("", middleware.CountBrowserConns(jsPing)).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(jsClientSideMiddlewareStack(clientSideStreamEvalRouter), middleware.Streaming)
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.Handle("/{user}", middleware.CountBrowserConns(jsPingWithUser)).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.Handle("", middleware.CountBrowserConns(jsPingWithUser)).Methods("REPORT", "OPTIONS")

	mobileEventsRouter := router.PathPrefix("/mobile").Subrouter()
	mobileEventsRouter.Use(mobileMiddlewareStack)
	mobileEventsRouter.Handle("/events/bulk", bulkEventHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	mobileEventsRouter.Handle("/events", bulkEventHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	mobileEventsRouter.Handle("", bulkEventHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	mobileEventsRouter.Handle("/events/diagnostic", bulkEventHandler(basictypes.MobileSDK, ldevents.DiagnosticEventDataKind, offlineMode)).Methods("POST")

	clientSideBulkEventsRouter := router.PathPrefix("/events/bulk/{envId}").Subrouter()
	clientSideBulkEventsRouter.Use(jsClientSideMiddlewareStack(clientSideBulkEventsRouter))
	clientSideBulkEventsRouter.Handle("", bulkEventHandler(basictypes.JSClientSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST", "OPTIONS")

	clientSideDiagnosticEventsRouter := router.PathPrefix("/events/diagnostic/{envId}").Subrouter()
	clientSideDiagnosticEventsRouter.Use(jsClientSideMiddlewareStack(clientSideBulkEventsRouter))
	clientSideDiagnosticEventsRouter.Handle("", bulkEventHandler(basictypes.JSClientSDK, ldevents.DiagnosticEventDataKind, offlineMode)).Methods("POST", "OPTIONS")

	clientSideImageEventsRouter := router.PathPrefix("/a/{envId}.gif").Subrouter()
	clientSideImageEventsRouter.Use(jsClientSideMiddlewareStack(clientSideImageEventsRouter))
	clientSideImageEventsRouter.HandleFunc("", getEventsImage).Methods("GET", "OPTIONS")

	serverSideRouter := router.PathPrefix("").Subrouter()
	serverSideRouter.Use(serverSideMiddlewareStack)
	serverSideRouter.Handle("/bulk", bulkEventHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	serverSideRouter.Handle("/diagnostic", bulkEventHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind, offlineMode)).Methods("POST")
	serverSideRouter.Handle("/all", middleware.CountServerConns(middleware.Streaming(
		streamHandler(r.serverSideStreamProvider, serverSideStreamLogMessage),
	))).Methods("GET")
	serverSideRouter.Handle("/flags", middleware.CountServerConns(middleware.Streaming(
		streamHandler(r.serverSideFlagsStreamProvider, serverSideFlagsOnlyStreamLogMessage),
	))).Methods("GET")

	return router
}
