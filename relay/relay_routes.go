package relay

import (
	"net/http"

	"context"
	"log/slog"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/logging"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/middleware"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"

	ct "github.com/launchdarkly/go-configtypes"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"

	"github.com/gorilla/mux"
	h "github.com/klauspost/compress/gzhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
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
// docs/endpoints.md. The {context} variable name is also load-bearing for tracing: it is how
// middleware.SanitizeRequestSpan recognizes a route that carries end-user data in the path.
func (r *Relay) makeRouter() *mux.Router {
	router := mux.NewRouter()
	router.Use(logging.ContextLoggerMiddleware(r.logger))
	if r.logger.Enabled(context.Background(), slog.LevelDebug) {
		router.Use(logging.RequestLoggerMiddleware(r.logger))
	}
	// The empty string tells otelmux to derive server.address and server.port from each request's
	// Host header. Its parameter is the "primary server name", not an instrumentation name: a
	// non-empty value overrides the real host on every span, so passing a service name here would
	// report that name as the address the client connected to.
	//
	// Relay records its own request metrics, with an attribute set trimmed of high-cardinality
	// dimensions, so otelmux's parallel HTTP server instruments must stay off. Without a meter
	// provider of its own, otelmux takes the global one, which is a no-op that starts delegating the
	// moment anything calls otel.SetMeterProvider. It would then report a second
	// http.server.request.duration, under its own scope and with its own buckets, keyed on the
	// client-controlled Host. Pinning a no-op provider keeps that from ever switching on.
	router.Use(otelmux.Middleware("", otelmux.WithMeterProvider(metricnoop.NewMeterProvider())))
	// Must come after the tracing middleware, which records the raw request path on the span it starts.
	router.Use(middleware.SanitizeRequestSpan)
	if r.config.HTTP.EnableCompression {
		router.Use(func(next http.Handler) http.Handler {
			return h.GzipHandler(next)
		})
	}
	// Status endpoints are not scoped to a single environment, so they are tracked in
	// http.server.active_requests without environment or platform attributes.
	statusMetrics := middleware.UnscopedActiveRequests(r.metricsManager, metrics.EndpointTypeStatus)
	router.Handle("/status", statusMetrics(statusHandler(r))).Methods("GET")
	// Register more specific routes first (with /filters/ literal)
	router.Handle("/status/{identifier}/filters/{filterKey}", statusMetrics(singleEnvironmentStatusHandler(r))).Methods("GET")
	router.Handle("/status/{projKey}/{envKey}/filters/{filterKey}", statusMetrics(singleEnvironmentStatusHandler(r))).Methods("GET")
	// Then register the general routes
	router.Handle("/status/{projKey}/{envKey}", statusMetrics(singleEnvironmentStatusHandler(r))).Methods("GET")
	router.Handle("/status/{identifier}", statusMetrics(singleEnvironmentStatusHandler(r))).Methods("GET")

	// gorilla/mux does not run router middleware for a request that matched no route, so the counting
	// for unmatched requests has to live inside the not-found handler itself.
	router.NotFoundHandler = middleware.UnscopedActiveRequests(r.metricsManager, metrics.EndpointTypeNotProvided)(
		http.NotFoundHandler())

	environmentGetters := relayEnvironmentGetters{r}
	sdkKeySelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.ServerSDK, environmentGetters)
	mobileKeySelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.MobileSDK, environmentGetters)
	jsClientSelector := middleware.SelectEnvironmentByAuthorizationKey(basictypes.JSClientSDK, environmentGetters)
	offlineMode := r.config.OfflineMode.FileDataSource != ""
	// Resolve the maximum client request body size once, applying the default when unset. This is always
	// defined so that REPORT evaluation bodies are bounded by default (customers may override or disable it).
	maxClientRequestBodySize := ct.NewOptBase2Bytes(
		r.config.Main.MaxClientRequestBodySize.GetOrElse(config.DefaultMaxClientRequestBodySize),
	)

	// Client-side evaluation (for JS, not mobile)
	// The endpointType parameter is what a request served by this stack reports as its
	// launchdarkly.relay.endpoint.type; the stack itself is shared across several kinds of endpoint.
	jsClientSideMiddlewareStack := func(subrouter *mux.Router, endpointType metrics.EndpointType) mux.MiddlewareFunc {
		return middleware.Chain(
			mux.CORSMethodMiddleware(subrouter),
			jsClientSelector, // selects an environment based on the client-side ID in the URL
			middleware.CORS,  // must apply this after jsClientSelector because the CORS headers can be environment-specific
			middleware.TrackUsageActivity(metrics.BrowserPlatformCategory),
			middleware.RequestMetrics(endpointType),
		)
	}

	goalsRouter := router.PathPrefix("/sdk/goals").Subrouter()
	goalsRouter.Use(jsClientSideMiddlewareStack(goalsRouter, metrics.EndpointTypeGoals))
	goalsRouter.HandleFunc("/{envId}", getGoals).Methods("GET", "OPTIONS")

	clientSideSdkEvalXRouter := router.PathPrefix("/sdk/evalx/{envId}/").Subrouter()
	clientSideSdkEvalXRouter.Use(jsClientSideMiddlewareStack(clientSideSdkEvalXRouter, metrics.EndpointTypePoll))
	clientSideSdkEvalXRouter.HandleFunc("/contexts/{context}", evaluateAllFeatureFlags(basictypes.JSClientSDK, maxClientRequestBodySize)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/context", evaluateAllFeatureFlags(basictypes.JSClientSDK, maxClientRequestBodySize)).Methods("REPORT", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/users/{context}", evaluateAllFeatureFlags(basictypes.JSClientSDK, maxClientRequestBodySize)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(basictypes.JSClientSDK, maxClientRequestBodySize)).Methods("REPORT", "OPTIONS")

	serverSideMiddlewareStack := func(endpointType metrics.EndpointType) mux.MiddlewareFunc {
		return middleware.Chain(
			sdkKeySelector,
			middleware.TrackUsageActivity(metrics.ServerPlatformCategory),
			middleware.RequestMetrics(endpointType),
		)
	}

	sdkRouter := router.PathPrefix("/sdk/").Subrouter()
	// (?)TODO: there is a bug in gorilla mux (see see https://github.com/gorilla/mux/pull/378) that means the middleware below
	// because it will not be run if it matches any earlier prefix.  Until it is fixed, we have to apply the middleware explicitly
	// sdkRouter.Use(sdkRouterMiddleware)

	// FDv2 server-side endpoints
	sdkRouter.Handle("/stream", serverSideMiddlewareStack(metrics.EndpointTypeStream)(middleware.UsageActivityStreamMonitoring(metrics.ServerPlatformCategory, middleware.CountServerConns(middleware.Streaming(
		streamHandlerV2(r.serverSideStreamProvider, serverSideStreamLogMessage),
	))))).Methods("GET")
	sdkRouter.Handle("/poll", serverSideMiddlewareStack(metrics.EndpointTypePoll)(middleware.ServerPollingRequestCount(http.HandlerFunc(pollHandlerV2)))).Methods("GET")

	// FDv2 client-side endpoints (unified mobile + JS client)
	clientSideFDv2EnvAuth := middleware.SelectEnvironmentByClientSideAuth(environmentGetters)

	clientSideFDv2StreamRouter := sdkRouter.PathPrefix("/stream/eval").Subrouter()
	clientSideFDv2StreamMiddleware := middleware.Chain(
		mux.CORSMethodMiddleware(clientSideFDv2StreamRouter),
		clientSideFDv2EnvAuth,
		middleware.CORS,
		middleware.DynamicTrackUsageActivity(),
		middleware.RequestMetrics(metrics.EndpointTypeStream),
	)
	clientSideFDv2StreamRouter.Use(clientSideFDv2StreamMiddleware, middleware.Streaming)
	clientSideFDv2PingHandler := pingStreamHandlerWithContextV2(r.mobileStreamProvider, r.jsClientStreamProvider, maxClientRequestBodySize)
	clientSideFDv2StreamRouter.Handle("/{context}", middleware.DynamicUsageActivityStreamMonitoring(middleware.CountClientConns(clientSideFDv2PingHandler))).Methods("GET", "OPTIONS")
	clientSideFDv2StreamRouter.Handle("", middleware.DynamicUsageActivityStreamMonitoring(middleware.CountClientConns(clientSideFDv2PingHandler))).Methods("POST", "OPTIONS")

	clientSideFDv2PollRouter := sdkRouter.PathPrefix("/poll/eval").Subrouter()
	clientSideFDv2PollMiddleware := middleware.Chain(
		mux.CORSMethodMiddleware(clientSideFDv2PollRouter),
		clientSideFDv2EnvAuth,
		middleware.CORS,
		middleware.DynamicTrackUsageActivity(),
		middleware.RequestMetrics(metrics.EndpointTypePoll),
	)
	clientSideFDv2PollRouter.Use(clientSideFDv2PollMiddleware)
	clientSideFDv2PollRouter.Handle("/{context}", middleware.DynamicPollingRequestCount(http.HandlerFunc(pollEvalHandlerV2(maxClientRequestBodySize)))).Methods("GET", "OPTIONS")
	clientSideFDv2PollRouter.Handle("", middleware.DynamicPollingRequestCount(http.HandlerFunc(pollEvalHandlerV2(maxClientRequestBodySize)))).Methods("POST", "OPTIONS")

	serverSidePollStack := serverSideMiddlewareStack(metrics.EndpointTypePoll)

	serverSideEvalXRouter := sdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.Handle("/contexts/{context}", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK, maxClientRequestBodySize))))).Methods("GET")
	serverSideEvalXRouter.Handle("/context", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK, maxClientRequestBodySize))))).Methods("REPORT")
	// /users and /user are obsolete names for /contexts and /context, still used by some supported SDKs; the handler is
	// the same, because in both cases LD accepts any valid user *or* context JSON.
	serverSideEvalXRouter.Handle("/users/{context}", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK, maxClientRequestBodySize))))).Methods("GET")
	serverSideEvalXRouter.Handle("/user", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(evaluateAllFeatureFlags(basictypes.ServerSDK, maxClientRequestBodySize))))).Methods("REPORT")

	// PHP SDK endpoints
	sdkRouter.Handle("/flags", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(pollAllFlagsHandler)))).Methods("GET")
	sdkRouter.Handle("/flags/{key}", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(pollFlagHandler)))).Methods("GET")
	sdkRouter.Handle("/segments/{key}", serverSidePollStack(middleware.ServerPollingRequestCount(http.HandlerFunc(pollSegmentHandler)))).Methods("GET")

	// Mobile evaluation
	mobileMiddlewareStack := func(endpointType metrics.EndpointType) mux.MiddlewareFunc {
		return middleware.Chain(
			mobileKeySelector,
			middleware.TrackUsageActivity(metrics.MobilePlatformCategory),
			middleware.RequestMetrics(endpointType))
	}

	msdkRouter := router.PathPrefix("/msdk/").Subrouter()
	msdkRouter.Use(mobileMiddlewareStack(metrics.EndpointTypePoll))

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/contexts/{context}", evaluateAllFeatureFlags(basictypes.MobileSDK, maxClientRequestBodySize)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/context", evaluateAllFeatureFlags(basictypes.MobileSDK, maxClientRequestBodySize)).Methods("REPORT")
	// /users and /user are obsolete names for /contexts and /context, still used by some supported SDKs; the handler is
	// the same, because in both cases LD accepts any valid user *or* context JSON.
	msdkEvalXRouter.HandleFunc("/users/{context}", evaluateAllFeatureFlags(basictypes.MobileSDK, maxClientRequestBodySize)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(basictypes.MobileSDK, maxClientRequestBodySize)).Methods("REPORT")

	mobileStreamRouter := router.PathPrefix("/meval").Subrouter()
	mobileStreamRouter.Use(mobileMiddlewareStack(metrics.EndpointTypeStream), middleware.Streaming)
	mobilePingWithUser := pingStreamHandlerWithContextV1(basictypes.MobileSDK, maxClientRequestBodySize, r.mobileStreamProvider)
	mobileStreamRouter.Handle("", middleware.UsageActivityStreamMonitoring(metrics.MobilePlatformCategory, middleware.CountMobileConns(mobilePingWithUser))).Methods("REPORT")
	mobileStreamRouter.Handle("/{context}", middleware.UsageActivityStreamMonitoring(metrics.MobilePlatformCategory, middleware.CountMobileConns(mobilePingWithUser))).Methods("GET")

	router.Handle("/mping", mobileMiddlewareStack(metrics.EndpointTypeStream)(
		middleware.UsageActivityStreamMonitoring(metrics.MobilePlatformCategory, middleware.CountMobileConns(middleware.Streaming(pingStreamHandlerV1(r.mobileStreamProvider)))))).Methods("GET")

	jsPing := pingStreamHandlerV1(r.jsClientStreamProvider)
	jsPingWithUser := pingStreamHandlerWithContextV1(basictypes.JSClientSDK, maxClientRequestBodySize, r.jsClientStreamProvider)

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(jsClientSideMiddlewareStack(clientSidePingRouter, metrics.EndpointTypeStream), middleware.Streaming)
	clientSidePingRouter.Handle("", middleware.UsageActivityStreamMonitoring(metrics.BrowserPlatformCategory, middleware.CountBrowserConns(jsPing))).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(jsClientSideMiddlewareStack(clientSideStreamEvalRouter, metrics.EndpointTypeStream), middleware.Streaming)
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.Handle("/{context}", middleware.UsageActivityStreamMonitoring(metrics.BrowserPlatformCategory, middleware.CountBrowserConns(jsPingWithUser))).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.Handle("", middleware.UsageActivityStreamMonitoring(metrics.BrowserPlatformCategory, middleware.CountBrowserConns(jsPingWithUser))).Methods("REPORT", "OPTIONS")

	mobileEventsRouter := router.PathPrefix("/mobile").Subrouter()
	mobileEventsRouter.Use(mobileMiddlewareStack(metrics.EndpointTypeEvents), middleware.GzipMiddleware(r.config.Events.MaxInboundPayloadSize), middleware.EventBytesMetrics())
	mobileEventsRouter.Handle("/events/bulk", bulkEventHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	mobileEventsRouter.Handle("/events", bulkEventHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	mobileEventsRouter.Handle("", bulkEventHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	mobileEventsRouter.Handle("/events/diagnostic", bulkEventHandler(basictypes.MobileSDK, ldevents.DiagnosticEventDataKind, offlineMode)).Methods("POST")

	clientSideBulkEventsRouter := router.PathPrefix("/events/bulk/{envId}").Subrouter()
	clientSideBulkEventsRouter.Use(jsClientSideMiddlewareStack(clientSideBulkEventsRouter, metrics.EndpointTypeEvents), middleware.GzipMiddleware(r.config.Events.MaxInboundPayloadSize), middleware.EventBytesMetrics())
	clientSideBulkEventsRouter.Handle("", bulkEventHandler(basictypes.JSClientSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST", "OPTIONS")

	clientSideDiagnosticEventsRouter := router.PathPrefix("/events/diagnostic/{envId}").Subrouter()
	clientSideDiagnosticEventsRouter.Use(jsClientSideMiddlewareStack(clientSideBulkEventsRouter, metrics.EndpointTypeEvents), middleware.GzipMiddleware(r.config.Events.MaxInboundPayloadSize), middleware.EventBytesMetrics())
	clientSideDiagnosticEventsRouter.Handle("", bulkEventHandler(basictypes.JSClientSDK, ldevents.DiagnosticEventDataKind, offlineMode)).Methods("POST", "OPTIONS")

	clientSideImageEventsRouter := router.PathPrefix("/a/{envId}.gif").Subrouter()
	clientSideImageEventsRouter.Use(jsClientSideMiddlewareStack(clientSideImageEventsRouter, metrics.EndpointTypeEvents))
	clientSideImageEventsRouter.HandleFunc("", getEventsImage).Methods("GET", "OPTIONS")

	serverSideRouter := router.PathPrefix("").Subrouter()

	// This subrouter carries both event ingestion and streaming endpoints, which report different
	// endpoint types, so the middleware stack is applied per endpoint group instead of to the whole
	// subrouter.
	serverSideBulkEventsRouter := serverSideRouter.NewRoute().Subrouter()
	serverSideBulkEventsRouter.Use(serverSideMiddlewareStack(metrics.EndpointTypeEvents), middleware.GzipMiddleware(r.config.Events.MaxInboundPayloadSize), middleware.EventBytesMetrics())
	serverSideBulkEventsRouter.Handle("/bulk", bulkEventHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind, offlineMode)).Methods("POST")
	serverSideBulkEventsRouter.Handle("/diagnostic", bulkEventHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind, offlineMode)).Methods("POST")

	serverSideStreamStack := serverSideMiddlewareStack(metrics.EndpointTypeStream)
	serverSideRouter.Handle("/all", serverSideStreamStack(middleware.UsageActivityStreamMonitoring(metrics.ServerPlatformCategory, middleware.CountServerConns(middleware.Streaming(
		streamHandlerV1(r.serverSideStreamProvider, serverSideStreamLogMessage),
	))))).Methods("GET")
	serverSideRouter.Handle("/flags", serverSideStreamStack(middleware.UsageActivityStreamMonitoring(metrics.ServerPlatformCategory, middleware.CountServerConns(middleware.Streaming(
		streamHandlerV1(r.serverSideFlagsStreamProvider, serverSideFlagsOnlyStreamLogMessage),
	))))).Methods("GET")

	return router
}

// Adapter that implements the middleware.RelayEnvironments interface to expose non-exported methods of Relay
type relayEnvironmentGetters struct {
	*Relay
}

func (r relayEnvironmentGetters) GetEnvironment(credential sdkauth.ScopedCredential) (env relayenv.EnvContext, err error) {
	return r.getEnvironment(credential)
}

func (r relayEnvironmentGetters) IsUnrecognizedEnvironment(err error) bool {
	return IsUnrecognizedEnvironment(err)
}

func (r relayEnvironmentGetters) IsNotReady(err error) bool {
	return IsNotReady(err)
}

func (r relayEnvironmentGetters) IsPayloadFilterNotFound(err error) bool {
	return IsPayloadFilterNotFound(err)
}
