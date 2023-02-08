package relay

import (
	"net/http"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/logging"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/middleware"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ldevents "github.com/launchdarkly/go-sdk-events/v2"

	"github.com/gorilla/mux"
)

const (
	serverSideStreamLogMessage          = "Application requested server-side /all stream"
	serverSideFlagsOnlyStreamLogMessage = "Application requested server-side /flags stream"
)

// makeRouter creates and configures a Router containing all of the standard routes for Relay.
//
// IMPORTANT: The route strings that are used here, such as "/sdk/evalx/{envId}/contexts/{context}", will appear
// in metrics data under the "route" tag if Relay is configured to export metrics. Therefore, we should use
// variable names like {envId} consistently and make sure they correspond to how the routes are shown in
// docs/endpoints.md.
func (r *Relay) makeRouter() *mux.Router {
	router := mux.NewRouter()
	router.Use(logging.GlobalContextLoggersMiddleware(r.loggers))
	if r.loggers.GetMinLevel() == ldlog.Debug {
		router.Use(logging.RequestLoggerMiddleware(r.loggers))
	}
	router.Handle("/status", statusHandler(r)).Methods("GET")

	environmentGetters := relayEnvironmentGetters{r}
	sdkKeySelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, environmentGetters)
	mobileKeySelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, environmentGetters)
	jsClientSelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, environmentGetters)
	offlineMode := r.config.OfflineMode.FileDataSource != ""

	// Client-side evaluation (for JS, not mobile)
	jsClientSideMiddlewareStack := func(subrouter *mux.Router) mux.MiddlewareFunc {
		return middleware.Chain(
			mux.CORSMethodMiddleware(subrouter),
			jsClientSelector, // selects an environment based on the client-side ID in the URL
			middleware.CORS,  // must apply this after jsClientSelector because the CORS headers can be environment-specific
			middleware.RequestCount(metrics.BrowserRequests),
		)
	}

	goalsRouter := router.PathPrefix("/sdk/goals").Subrouter()
	goalsRouter.Use(jsClientSideMiddlewareStack(goalsRouter))
	goalsRouter.HandleFunc("/{envId}", getGoals).Methods("GET", "OPTIONS")

	clientSideSdkEvalXRouter := router.PathPrefix("/sdk/evalx/{envId}/").Subrouter()
	clientSideSdkEvalXRouter.Use(jsClientSideMiddlewareStack(clientSideSdkEvalXRouter))
	clientSideSdkEvalXRouter.HandleFunc("/contexts/{context}", evaluateAllFeatureFlags(basictypes.JSClientSDK)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/context", evaluateAllFeatureFlags(basictypes.JSClientSDK)).Methods("REPORT", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/users/{context}", evaluateAllFeatureFlags(basictypes.JSClientSDK)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(basictypes.JSClientSDK)).Methods("REPORT", "OPTIONS")

	serverSideMiddlewareStack := middleware.Chain(
		sdkKeySelector,
		middleware.RequestCount(metrics.ServerRequests))

	serverSideSdkRouter := router.PathPrefix("/sdk/").Subrouter()
	// (?)TODO: there is a bug in gorilla mux (see see https://github.com/gorilla/mux/pull/378) that means the middleware below
	// because it will not be run if it matches any earlier prefix.  Until it is fixed, we have to apply the middleware explicitly
	// serverSideSdkRouter.Use(serverSideMiddlewareStack)

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.Handle("/contexts/{context}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK)))).Methods("GET")
	serverSideEvalXRouter.Handle("/context", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK)))).Methods("REPORT")
	// /users and /user are obsolete names for /contexts and /context, still used by some supported SDKs; the handler is
	// the same, because in both cases LD accepts any valid user *or* context JSON.
	serverSideEvalXRouter.Handle("/users/{context}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK)))).Methods("GET")
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

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/contexts/{context}", evaluateAllFeatureFlags(basictypes.MobileSDK)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/context", evaluateAllFeatureFlags(basictypes.MobileSDK)).Methods("REPORT")
	// /users and /user are obsolete names for /contexts and /context, still used by some supported SDKs; the handler is
	// the same, because in both cases LD accepts any valid user *or* context JSON.
	msdkEvalXRouter.HandleFunc("/users/{context}", evaluateAllFeatureFlags(basictypes.MobileSDK)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(basictypes.MobileSDK)).Methods("REPORT")

	mobileStreamRouter := router.PathPrefix("/meval").Subrouter()
	mobileStreamRouter.Use(mobileMiddlewareStack, middleware.Streaming)
	mobilePingWithUser := pingStreamHandlerWithContext(basictypes.MobileSDK, r.mobileStreamProvider)
	mobileStreamRouter.Handle("", middleware.CountMobileConns(mobilePingWithUser)).Methods("REPORT")
	mobileStreamRouter.Handle("/{context}", middleware.CountMobileConns(mobilePingWithUser)).Methods("GET")

	router.Handle("/mping", mobileKeySelector(
		middleware.CountMobileConns(middleware.Streaming(pingStreamHandler(r.mobileStreamProvider))))).Methods("GET")

	jsPing := pingStreamHandler(r.jsClientStreamProvider)
	jsPingWithUser := pingStreamHandlerWithContext(basictypes.JSClientSDK, r.jsClientStreamProvider)

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(jsClientSideMiddlewareStack(clientSidePingRouter), middleware.Streaming)
	clientSidePingRouter.Handle("", middleware.CountBrowserConns(jsPing)).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(jsClientSideMiddlewareStack(clientSideStreamEvalRouter), middleware.Streaming)
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.Handle("/{context}", middleware.CountBrowserConns(jsPingWithUser)).Methods("GET", "OPTIONS")
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

// Adapter that implements the middleware.RelayEnvironments interface to expose non-exported methods of Relay
type relayEnvironmentGetters struct {
	*Relay
}

func (r relayEnvironmentGetters) GetEnvironment(credential config.SDKCredential) (env relayenv.EnvContext, fullyConfigured bool) {
	return r.getEnvironment(credential)
}

func (r relayEnvironmentGetters) GetAllEnvironments() []relayenv.EnvContext {
	return r.getAllEnvironments()
}
