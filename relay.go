package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	redigo "github.com/garyburd/redigo/redis"
	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v1/ldvalue"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldconsul"
	"gopkg.in/launchdarkly/go-server-sdk.v4/lddynamodb"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
	ldr "gopkg.in/launchdarkly/go-server-sdk.v4/redis"

	"gopkg.in/launchdarkly/ld-relay.v5/httpconfig"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/metrics"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/store"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/version"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

const (
	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"
)

// InitializeMetrics reads a MetricsConfig and registers OpenCensus exporters for all configured options. Will only initialize exporters on the first call to InitializeMetrics.
func InitializeMetrics(c MetricsConfig) error {
	return metrics.RegisterExporters(c.toOptions())
}

type environmentStatus struct {
	SdkKey    string `json:"sdkKey"`
	EnvId     string `json:"envId,omitempty"`
	MobileKey string `json:"mobileKey,omitempty"`
	Status    string `json:"status"`
}

// LdClientContext defines a minimal interface for a LaunchDarkly client
type LdClientContext interface {
	Initialized() bool
}

type clientHandlers struct {
	flagsStreamHandler http.Handler
	allStreamHandler   http.Handler
	pingStreamHandler  http.Handler
	eventDispatcher    *events.EventDispatcher
}

type clientContext interface {
	getClient() LdClientContext
	setClient(LdClientContext)
	getStore() ld.FeatureStore
	getLoggers() *ldlog.Loggers
	getHandlers() clientHandlers
	getMetricsCtx() context.Context
	getTtl() time.Duration
	getInitError() error
}

type clientContextImpl struct {
	mu         sync.RWMutex
	client     LdClientContext
	store      ld.FeatureStore
	loggers    ldlog.Loggers
	handlers   clientHandlers
	sdkKey     string
	envId      *string
	mobileKey  *string
	name       string
	metricsCtx context.Context
	ttl        time.Duration
	initErr    error
}

// Relay relays endpoints to and from the LaunchDarkly service
type Relay struct {
	http.Handler
	sdkClientMux    clientMux
	mobileClientMux clientMux
	clientSideMux   clientSideMux
}

type evalXResult struct {
	Value                ldvalue.Value                 `json:"value"`
	Variation            *int                          `json:"variation,omitempty"`
	Version              int                           `json:"version"`
	DebugEventsUntilDate *uint64                       `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool                          `json:"trackEvents,omitempty"`
	TrackReason          bool                          `json:"trackReason,omitempty"`
	Reason               *ld.EvaluationReasonContainer `json:"reason,omitempty"`
}

type sdkKind string

const (
	serverSdk   sdkKind = "server"
	jsClientSdk sdkKind = "js"
	mobileSdk   sdkKind = "mobile"
)

func (c *clientContextImpl) getClient() LdClientContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

func (c *clientContextImpl) setClient(client LdClientContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
}

func (c *clientContextImpl) getStore() ld.FeatureStore {
	return c.store
}

func (c *clientContextImpl) getLoggers() *ldlog.Loggers {
	return &c.loggers
}

func (c *clientContextImpl) getHandlers() clientHandlers {
	return c.handlers
}

func (c *clientContextImpl) getMetricsCtx() context.Context {
	return c.metricsCtx
}

func (c *clientContextImpl) getTtl() time.Duration {
	return c.ttl
}

func (c *clientContextImpl) getInitError() error {
	return c.initErr
}

type clientFactoryFunc func(sdkKey string, config ld.Config) (LdClientContext, error)

// DefaultClientFactory creates a default client for connecting to the LaunchDarkly stream
func DefaultClientFactory(sdkKey string, config ld.Config) (LdClientContext, error) {
	return ld.MakeCustomClient(sdkKey, config, time.Second*10)
}

// NewRelay creates a new relay given a configuration and a method to create a client
func NewRelay(c Config, clientFactory clientFactoryFunc) (*Relay, error) {
	logging.InitLoggingWithLevel(c.Main.GetLogLevel())

	allPublisher := eventsource.NewServer()
	allPublisher.Gzip = false
	allPublisher.AllowCORS = true
	allPublisher.ReplayAll = true
	flagsPublisher := eventsource.NewServer()
	flagsPublisher.Gzip = false
	flagsPublisher.AllowCORS = true
	flagsPublisher.ReplayAll = true
	pingPublisher := eventsource.NewServer()
	pingPublisher.Gzip = false
	pingPublisher.AllowCORS = true
	pingPublisher.ReplayAll = true
	clients := map[string]*clientContextImpl{}
	mobileClients := map[string]*clientContextImpl{}

	clientSideMux := clientSideMux{
		contextByKey: map[string]*clientSideContext{},
	}

	if len(c.Environment) == 0 {
		return nil, fmt.Errorf("you must specify at least one environment in your configuration")
	}

	baseUrl, err := url.Parse(c.Main.BaseUri)
	if err != nil {
		return nil, fmt.Errorf(`unable to parse baseUri "%s"`, c.Main.BaseUri)
	}

	httpConfig, err := httpconfig.NewHTTPConfig(c.Proxy)
	if err != nil {
		return nil, err
	}

	clientReadyCh := make(chan clientContext, len(c.Environment))

	for envName, envConfig := range c.Environment {
		clientContext, err := newClientContext(envName, envConfig, c, clientFactory, httpConfig, allPublisher, flagsPublisher, pingPublisher, clientReadyCh)
		if err != nil {
			return nil, fmt.Errorf(`unable to create client context for "%s": %s`, envName, err)
		}
		clients[envConfig.SdkKey] = clientContext
		if envConfig.MobileKey != nil && *envConfig.MobileKey != "" {
			mobileClients[*envConfig.MobileKey] = clientContext
		}

		if envConfig.EnvId != nil && *envConfig.EnvId != "" {
			var allowedOrigins []string
			if envConfig.AllowedOrigin != nil && len(*envConfig.AllowedOrigin) != 0 {
				allowedOrigins = *envConfig.AllowedOrigin
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

			clientSideMux.contextByKey[*envConfig.EnvId] = &clientSideContext{
				clientContext:  clientContext,
				proxy:          proxy,
				allowedOrigins: allowedOrigins,
			}
		}
	}

	r := Relay{
		sdkClientMux:    clientMux{clientContextByKey: clients},
		mobileClientMux: clientMux{clientContextByKey: mobileClients},
		clientSideMux:   clientSideMux,
	}

	if c.Main.ExitAlways {
		logging.GlobalLoggers.Info("Running in one-shot mode - will exit immediately after initializing environments")
		// Just wait until all clients have either started or failed, then exit without bothering
		// to set up HTTP handlers.
		numFinished := 0
		failed := false
		for numFinished < len(c.Environment) {
			ctx := <-clientReadyCh
			numFinished++
			if ctx.getInitError() != nil {
				failed = true
			}
		}
		var err error
		if failed {
			err = errors.New("one or more environments failed to initialize")
		}
		return &r, err
	}
	r.Handler = r.makeHandler(c.Main.GetLogLevel() <= ldlog.Debug)
	return &r, nil
}

func (r *Relay) makeHandler(withRequestLogging bool) http.Handler {
	router := mux.NewRouter()
	if withRequestLogging {
		router.Use(logging.RequestLoggerMiddleware)
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
		r.sdkClientMux.selectClientByAuthorizationKey,
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
		r.mobileClientMux.selectClientByAuthorizationKey,
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
	mobileStreamRouter.Handle("", countMobileConns(pingStreamHandler())).Methods("REPORT")
	mobileStreamRouter.Handle("/{user}", countMobileConns(pingStreamHandler())).Methods("GET")

	router.Handle("/mping", r.mobileClientMux.selectClientByAuthorizationKey(
		countMobileConns(streamingMiddleware(pingStreamHandler())))).Methods("GET")

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSidePingRouter), streamingMiddleware)
	clientSidePingRouter.Handle("", countBrowserConns(pingStreamHandler())).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideStreamEvalRouter), streamingMiddleware)
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.Handle("/{user}", countBrowserConns(pingStreamHandler())).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.Handle("", countBrowserConns(pingStreamHandler())).Methods("REPORT", "OPTIONS")

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

func newClientContext(envName string, envConfig *EnvConfig, c Config, clientFactory clientFactoryFunc,
	httpConfig httpconfig.HTTPConfig, allPublisher, flagsPublisher, pingPublisher *eventsource.Server,
	readyCh chan<- clientContext) (*clientContextImpl, error) {

	envLoggers := logging.MakeLoggers(fmt.Sprintf("env: %s", envName))
	if envConfig.LogLevel == "" {
		envLoggers.SetMinLevel(c.Main.GetLogLevel())
	} else {
		envLoggers.SetMinLevel(envConfig.GetLogLevel())
	}

	baseFeatureStoreFactory, err := createFeatureStore(c, envConfig, envLoggers)
	if err != nil {
		return nil, err
	}

	clientConfig := ld.DefaultConfig
	clientConfig.Stream = true
	clientConfig.StreamUri = c.Main.StreamUri
	clientConfig.BaseUri = c.Main.BaseUri
	clientConfig.Loggers = envLoggers
	clientConfig.UserAgent = "LDRelay/" + version.Version
	clientConfig.HTTPClientFactory = httpConfig.HTTPClientFactory

	// This is a bit awkward because the SDK now uses a factory mechanism to create feature stores - so that they can
	// inherit the client's logging settings - but we also need to be able to access the feature store instance
	// directly. So we're calling the factory ourselves, and still using the deprecated Config.FeatureStore property.
	baseFeatureStore, err := baseFeatureStoreFactory(clientConfig)
	if err != nil {
		return nil, err
	}
	clientConfig.FeatureStore = store.NewSSERelayFeatureStore(envConfig.SdkKey, allPublisher, flagsPublisher, pingPublisher,
		baseFeatureStore, envLoggers, c.Main.HeartbeatIntervalSecs)

	var eventDispatcher *events.EventDispatcher
	if c.Events.SendEvents {
		envLoggers.Info("Proxying events for this environment")
		eventDispatcher = events.NewEventDispatcher(envConfig.SdkKey, envConfig.MobileKey, envConfig.EnvId,
			envLoggers, c.Events, httpConfig, baseFeatureStore)
	}

	eventsPublisher, err := events.NewHttpEventPublisher(envConfig.SdkKey, envLoggers,
		events.OptionUri(c.Events.EventsUri),
		events.OptionClient{Client: httpConfig.Client()})
	if err != nil {
		return nil, fmt.Errorf("unable to create publisher: %s", err)
	}

	m, err := metrics.NewMetricsProcessor(eventsPublisher, metrics.OptionEnvName(envName))
	if err != nil {
		return nil, fmt.Errorf("unable to create metrics processor: %s", err)
	}

	clientContext := &clientContextImpl{
		name:       envName,
		envId:      envConfig.EnvId,
		sdkKey:     envConfig.SdkKey,
		mobileKey:  envConfig.MobileKey,
		store:      baseFeatureStore,
		loggers:    envLoggers,
		metricsCtx: m.OpenCensusCtx,
		ttl:        time.Minute * time.Duration(envConfig.TtlMinutes),
		handlers: clientHandlers{
			eventDispatcher:    eventDispatcher,
			allStreamHandler:   allPublisher.Handler(envConfig.SdkKey),
			flagsStreamHandler: flagsPublisher.Handler(envConfig.SdkKey),
			pingStreamHandler:  pingPublisher.Handler(envConfig.SdkKey),
		},
	}

	// Connecting may take time, so do this in parallel
	go func(envName string, envConfig EnvConfig) {
		client, err := clientFactory(envConfig.SdkKey, clientConfig)
		clientContext.setClient(client)

		if err != nil {
			clientContext.initErr = err
			if !c.Main.IgnoreConnectionErrors {
				envLoggers.Errorf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

				if c.Main.ExitOnError {
					os.Exit(1)
				}
				if readyCh != nil {
					readyCh <- clientContext
				}
				return
			}

			logging.GlobalLoggers.Errorf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
		} else {
			logging.GlobalLoggers.Infof("Initialized LaunchDarkly client for %s\n", envName)
		}
		if readyCh != nil {
			readyCh <- clientContext
		}
	}(envName, *envConfig)

	return clientContext, nil
}

func createFeatureStore(c Config, envConfig *EnvConfig, loggers ldlog.Loggers) (ld.FeatureStoreFactory, error) {
	infoLogger := loggers.ForLevel(ldlog.Info) // for methods that require a single logger instead of a Loggers
	useRedis := c.Redis.Url != "" || c.Redis.Host != ""
	useConsul := c.Consul.Host != ""
	useDynamoDB := c.DynamoDB.Enabled
	countTrue := func(values ...bool) int {
		n := 0
		for _, v := range values {
			if v {
				n++
			}
		}
		return n
	}
	if countTrue(useRedis, useConsul, useDynamoDB) > 1 {
		return nil, errors.New("Cannot enable more than one database at a time (Redis, DynamoDB, Consul)")
	}
	if useRedis {
		redisURL := c.Redis.Url
		if c.Redis.Host != "" {
			if redisURL != "" {
				loggers.Warnf("Both a URL and a hostname were specified for Redis; will use the URL")
			} else {
				port := c.Redis.Port
				if port == 0 {
					port = 6379
				}
				redisURL = fmt.Sprintf("redis://%s:%d", c.Redis.Host, port)
			}
		}
		loggers.Infof("Using Redis feature store: %s with prefix: %s\n", redisURL, envConfig.Prefix)
		redisOptions := []ldr.FeatureStoreOption{ldr.Prefix(envConfig.Prefix),
			ldr.CacheTTL(time.Duration(c.Redis.LocalTtl) * time.Millisecond), ldr.Logger(infoLogger)}
		dialOptions := []redigo.DialOption{}
		if c.Redis.Tls || (c.Redis.Password != "") {
			if c.Redis.Tls {
				if strings.HasPrefix(redisURL, "redis:") {
					// Redigo's DialUseTLS option will not work if you're specifying a URL.
					redisURL = "rediss:" + strings.TrimPrefix(redisURL, "redis:")
				}
			}
			if c.Redis.Password != "" {
				dialOptions = append(dialOptions, redigo.DialPassword(c.Redis.Password))
			}
		}
		redisOptions = append(redisOptions, ldr.URL(redisURL), ldr.DialOptions(dialOptions...))
		return ldr.NewRedisFeatureStoreFactory(redisOptions...)
	}
	if useConsul {
		loggers.Infof("Using Consul feature store: %s with prefix: %s", c.Consul.Host, envConfig.Prefix)
		return ldconsul.NewConsulFeatureStoreFactory(ldconsul.Address(c.Consul.Host), ldconsul.Prefix(envConfig.Prefix),
			ldconsul.CacheTTL(time.Duration(c.Consul.LocalTtl)*time.Millisecond), ldconsul.Logger(infoLogger))
	}
	if useDynamoDB {
		// Note that the global TableName can be omitted if you specify a TableName for each environment
		// (this is why we need an Enabled property here, since the other properties are all optional).
		// You can also specify a prefix for each environment, as with the other databases.
		tableName := envConfig.TableName
		if tableName == "" {
			tableName = c.DynamoDB.TableName
		}
		if tableName == "" {
			return nil, errors.New("TableName property must be specified for DynamoDB, either globally or per environment")
		}
		loggers.Infof("Using DynamoDB feature store: %s with prefix: %s", tableName, envConfig.Prefix)
		options := []lddynamodb.FeatureStoreOption{
			lddynamodb.Prefix(envConfig.Prefix),
			lddynamodb.CacheTTL(time.Duration(c.DynamoDB.LocalTtl) * time.Millisecond),
			lddynamodb.Logger(infoLogger),
		}
		if c.DynamoDB.Url != "" {
			awsOptions := session.Options{
				Config: aws.Config{
					Endpoint: aws.String(c.DynamoDB.Url),
				},
			}
			options = append(options, lddynamodb.SessionOptions(awsOptions))
		}
		return lddynamodb.NewDynamoDBFeatureStoreFactory(tableName, options...)
	}
	return ld.NewInMemoryFeatureStoreFactory(), nil
}
