package relay

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
)

const (
	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"
)

type environmentStatus struct {
	SdkKey    string `json:"sdkKey"`
	EnvId     string `json:"envId,omitempty"`
	MobileKey string `json:"mobileKey,omitempty"`
	Status    string `json:"status"`
}

// Relay relays endpoints to and from the LaunchDarkly service
type Relay struct {
	http.Handler
	sdkClientMux    clientMux
	mobileClientMux clientMux
	clientSideMux   clientSideMux
	metricsManager  *metrics.Manager
	config          config.Config
	loggers         ldlog.Loggers
}

type evalXResult struct {
	Value                ldvalue.Value               `json:"value"`
	Variation            *int                        `json:"variation,omitempty"`
	Version              int                         `json:"version"`
	DebugEventsUntilDate *ldtime.UnixMillisecondTime `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool                        `json:"trackEvents,omitempty"`
	TrackReason          bool                        `json:"trackReason,omitempty"`
	Reason               *ldreason.EvaluationReason  `json:"reason,omitempty"`
}

// NewRelay creates a new relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory sdkconfig.ClientFactoryFunc) (*Relay, error) {
	if err := config.ValidateConfig(&c, loggers); err != nil { // in case a not-yet-validated Config was passed to NewRelay
		return nil, err
	}

	if c.Main.LogLevel.IsDefined() {
		loggers.SetMinLevel(c.Main.LogLevel.GetOrElse(ldlog.Info))
	}

	metricsManager, err := metrics.NewManager(c.MetricsConfig, 0, loggers)
	if err != nil {
		return nil, fmt.Errorf("unable to create metrics manager: %s", err)
	}

	makeSSEServer := func() *eventsource.Server {
		s := eventsource.NewServer()
		s.Gzip = false
		s.AllowCORS = true
		s.ReplayAll = true
		s.MaxConnTime = c.Main.MaxClientConnectionTime.GetOrElse(0)
		return s
	}
	allPublisher := makeSSEServer()
	flagsPublisher := makeSSEServer()
	pingPublisher := makeSSEServer()
	clients := make(map[config.SDKCredential]relayenv.EnvContext)
	mobileClients := make(map[config.SDKCredential]relayenv.EnvContext)

	clientSideMux := clientSideMux{
		contextByKey: map[config.SDKCredential]*clientSideContext{},
	}

	if len(c.Environment) == 0 {
		return nil, fmt.Errorf("you must specify at least one environment in your configuration")
	}

	baseUrl := c.Main.BaseURI.Get()
	if baseUrl == nil {
		baseUrl, err = url.Parse(config.DefaultBaseURI)
		if err != nil {
			return nil, errors.New("unexpected error: default base URI is invalid")
		}
	}

	clientReadyCh := make(chan relayenv.EnvContext, len(c.Environment))

	for envName, envConfigPtr := range c.Environment {
		var envConfig config.EnvConfig
		if envConfigPtr != nil { // this is a pointer only because that's how gcfg works; should not be nil
			envConfig = *envConfigPtr
		}

		dataStoreFactory, err := sdkconfig.ConfigureDataStore(c, envConfig, loggers)
		if err != nil {
			return nil, err
		}

		clientContext, err := relayenv.NewEnvContext(
			envName,
			envConfig,
			c,
			clientFactory,
			dataStoreFactory,
			allPublisher,
			flagsPublisher,
			pingPublisher,
			metricsManager,
			loggers,
			clientReadyCh,
		)
		if err != nil {
			return nil, fmt.Errorf(`unable to create client context for "%s": %s`, envName, err)
		}
		clients[envConfig.SDKKey] = clientContext
		if envConfig.MobileKey != "" {
			mobileClients[envConfig.MobileKey] = clientContext
		}

		if envConfig.EnvID != "" {
			var allowedOrigins []string
			if len(envConfig.AllowedOrigin) != 0 {
				allowedOrigins = envConfig.AllowedOrigin
			}
			cachingTransport := httpcache.NewMemoryCacheTransport()
			if envConfig.InsecureSkipVerify {
				transport := &(*http.DefaultTransport.(*http.Transport))
				transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: envConfig.InsecureSkipVerify} // nolint:gas // allow this because the user has to explicitly enable it
				cachingTransport.Transport = transport
			}

			proxy := &httputil.ReverseProxy{
				Director: func(r *http.Request) {
					url := r.URL
					url.Scheme = baseUrl.Scheme
					url.Host = baseUrl.Host
					r.Host = baseUrl.Hostname()
				},
				ModifyResponse: func(r *http.Response) error {
					// Leave access control to our own cors middleware
					for h := range r.Header {
						if strings.HasPrefix(strings.ToLower(h), "access-control") {
							r.Header.Del(h)
						}
					}
					return nil
				},
				Transport: cachingTransport,
			}

			clientSideMux.contextByKey[envConfig.EnvID] = &clientSideContext{
				EnvContext:     clientContext,
				proxy:          proxy,
				allowedOrigins: allowedOrigins,
			}
		}
	}

	r := Relay{
		sdkClientMux:    clientMux{clientContextByKey: clients},
		mobileClientMux: clientMux{clientContextByKey: mobileClients},
		clientSideMux:   clientSideMux,
		metricsManager:  metricsManager,
		config:          c,
		loggers:         loggers,
	}

	if c.Main.ExitAlways {
		loggers.Info("Running in one-shot mode - will exit immediately after initializing environments")
		// Just wait until all clients have either started or failed, then exit without bothering
		// to set up HTTP handlers.
		numFinished := 0
		failed := false
		for numFinished < len(c.Environment) {
			ctx := <-clientReadyCh
			numFinished++
			if ctx.GetInitError() != nil {
				failed = true
			}
		}
		var err error
		if failed {
			err = errors.New("one or more environments failed to initialize")
		}
		return &r, err
	}
	isDebugLoggingEnabled := c.Main.LogLevel.GetOrElse(ldlog.Info) <= ldlog.Debug
	r.Handler = r.makeHandler(isDebugLoggingEnabled)
	return &r, nil
}

// Close shuts down components created by the Relay.
//
// Currently this includes only the metrics components; it does not close SDK clients.
func (r *Relay) Close() error {
	r.metricsManager.Close()
	return nil
}

func (r *Relay) makeHandler(withRequestLogging bool) http.Handler {
	router := mux.NewRouter()
	router.Use(logging.GlobalContextLoggersMiddleware(r.loggers))
	if withRequestLogging {
		router.Use(logging.RequestLoggerMiddleware(r.loggers))
	}
	router.HandleFunc("/status", r.sdkClientMux.getStatus).Methods("GET")

	// Client-side evaluation
	clientSideMiddlewareStack := chainMiddleware(
		corsMiddleware,
		r.clientSideMux.selectClientByUrlParam,
		requestCountMiddleware(metrics.BrowserRequests))

	goalsRouter := router.PathPrefix("/sdk/goals").Subrouter()
	goalsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(goalsRouter))
	goalsRouter.HandleFunc("/{envId}", r.clientSideMux.getGoals).Methods("GET", "OPTIONS")

	clientSideSdkEvalRouter := router.PathPrefix("/sdk/eval/{envId}/").Subrouter()
	clientSideSdkEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideSdkEvalRouter))
	clientSideSdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly(jsClientSdk)).Methods("GET", "OPTIONS")
	clientSideSdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly(jsClientSdk)).Methods("REPORT", "OPTIONS")

	clientSideSdkEvalXRouter := router.PathPrefix("/sdk/evalx/{envId}/").Subrouter()
	clientSideSdkEvalXRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideSdkEvalXRouter))
	clientSideSdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags(jsClientSdk)).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(jsClientSdk)).Methods("REPORT", "OPTIONS")

	serverSideMiddlewareStack := chainMiddleware(
		r.sdkClientMux.selectClientByAuthorizationKey(serverSdk),
		requestCountMiddleware(metrics.ServerRequests))

	serverSideSdkRouter := router.PathPrefix("/sdk/").Subrouter()
	// (?)TODO: there is a bug in gorilla mux (see see https://github.com/gorilla/mux/pull/378) that means the middleware below
	// because it will not be run if it matches any earlier prefix.  Until it is fixed, we have to apply the middleware explicitly
	// serverSideSdkRouter.Use(serverSideMiddlewareStack)

	serverSideEvalRouter := serverSideSdkRouter.PathPrefix("/eval/").Subrouter()
	serverSideEvalRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(serverSdk)))).Methods("GET")
	serverSideEvalRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(serverSdk)))).Methods("REPORT")

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(serverSdk)))).Methods("GET")
	serverSideEvalXRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags(serverSdk)))).Methods("REPORT")

	// PHP SDK endpoints
	serverSideSdkRouter.Handle("/flags", serverSideMiddlewareStack(http.HandlerFunc(pollAllFlagsHandler))).Methods("GET")
	serverSideSdkRouter.Handle("/flags/{key}", serverSideMiddlewareStack(http.HandlerFunc(pollFlagHandler))).Methods("GET")
	serverSideSdkRouter.Handle("/segments/{key}", serverSideMiddlewareStack(http.HandlerFunc(pollSegmentHandler))).Methods("GET")

	// Mobile evaluation
	mobileMiddlewareStack := chainMiddleware(
		r.mobileClientMux.selectClientByAuthorizationKey(mobileSdk),
		requestCountMiddleware(metrics.MobileRequests))

	msdkRouter := router.PathPrefix("/msdk/").Subrouter()
	msdkRouter.Use(mobileMiddlewareStack)

	msdkEvalRouter := msdkRouter.PathPrefix("/eval/").Subrouter()
	msdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly(mobileSdk)).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly(mobileSdk)).Methods("REPORT")

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags(mobileSdk)).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags(mobileSdk)).Methods("REPORT")

	mobileStreamRouter := router.PathPrefix("/meval").Subrouter()
	mobileStreamRouter.Use(mobileMiddlewareStack, streamingMiddleware)
	mobileStreamRouter.Handle("", countMobileConns(pingStreamHandlerWithUser(mobileSdk))).Methods("REPORT")
	mobileStreamRouter.Handle("/{user}", countMobileConns(pingStreamHandlerWithUser(mobileSdk))).Methods("GET")

	router.Handle("/mping", r.mobileClientMux.selectClientByAuthorizationKey(mobileSdk)(
		countMobileConns(streamingMiddleware(pingStreamHandler())))).Methods("GET")

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSidePingRouter), streamingMiddleware)
	clientSidePingRouter.Handle("", countBrowserConns(pingStreamHandler())).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideStreamEvalRouter), streamingMiddleware)
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.Handle("/{user}", countBrowserConns(pingStreamHandlerWithUser(jsClientSdk))).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.Handle("", countBrowserConns(pingStreamHandlerWithUser(jsClientSdk))).Methods("REPORT", "OPTIONS")

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
	serverSideRouter.Handle("/all", countServerConns(streamingMiddleware(allStreamHandler()))).Methods("GET")
	serverSideRouter.Handle("/flags", countServerConns(streamingMiddleware(flagsStreamHandler()))).Methods("GET")

	return router
}
