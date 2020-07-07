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

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	redigo "github.com/garyburd/redigo/redis"
	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldconsul"
	"gopkg.in/launchdarkly/go-server-sdk.v5/lddynamodb"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldredis"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
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
	getStore() interfaces.DataStore
	getLoggers() ldlog.Loggers
	getHandlers() clientHandlers
	getMetricsEnvironment() *metrics.EnvironmentManager
	getMetricsContext() context.Context
	getTtl() time.Duration
	getInitError() error
}

type clientContextImpl struct {
	mu           sync.RWMutex
	client       LdClientContext
	storeAdapter *store.SSERelayDataStoreAdapter
	loggers      ldlog.Loggers
	handlers     clientHandlers
	sdkKey       string
	envId        *string
	mobileKey    *string
	name         string
	metricsEnv   *metrics.EnvironmentManager
	ttl          time.Duration
	initErr      error
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

func (c *clientContextImpl) getStore() interfaces.DataStore {
	if c.storeAdapter == nil {
		return nil
	}
	return c.storeAdapter.GetStore()
}

func (c *clientContextImpl) getLoggers() ldlog.Loggers {
	return c.loggers
}

func (c *clientContextImpl) getHandlers() clientHandlers {
	return c.handlers
}

func (c *clientContextImpl) getMetricsEnvironment() *metrics.EnvironmentManager {
	return c.metricsEnv
}

func (c *clientContextImpl) getMetricsContext() context.Context {
	return c.metricsEnv.GetOpenCensusContext()
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

// NewRelay creates a new relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory clientFactoryFunc) (*Relay, error) {
	if c.Main.LogLevel != "" {
		loggers.SetMinLevel(c.Main.GetLogLevel())
	}

	metricsManager, err := metrics.NewManager(c.MetricsConfig, 0, loggers)
	if err != nil {
		return nil, fmt.Errorf("unable to create metrics manager: %s", err)
	}

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

	clientReadyCh := make(chan clientContext, len(c.Environment))

	for envName, envConfig := range c.Environment {
		httpConfig, err := httpconfig.NewHTTPConfig(c.Proxy, envConfig.SdkKey, loggers)
		if err != nil {
			return nil, err
		}

		clientContext, err := newClientContext(
			envName,
			envConfig,
			c,
			clientFactory,
			httpConfig,
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

func newClientContext(
	envName string,
	envConfig *config.EnvConfig,
	c config.Config,
	clientFactory clientFactoryFunc,
	httpConfig httpconfig.HTTPConfig,
	allPublisher, flagsPublisher, pingPublisher *eventsource.Server,
	metricsManager *metrics.Manager,
	loggers ldlog.Loggers,
	readyCh chan<- clientContext,
) (*clientContextImpl, error) {
	envLoggers := loggers
	envLoggers.SetPrefix(fmt.Sprintf("[env: %s]", envName))
	if envConfig.LogLevel != "" {
		envLoggers.SetMinLevel(envConfig.GetLogLevel())
	}

	baseDataStoreFactory, err := configureDataStore(c, envConfig, envLoggers)
	if err != nil {
		return nil, err
	}

	clientConfig := ld.Config{
		DataSource: ldcomponents.StreamingDataSource().BaseURI(c.Main.StreamUri),
		HTTP:       httpConfig.SDKHTTPConfigFactory,
		Logging:    ldcomponents.Logging().Loggers(envLoggers),
	}

	storeAdapter := store.NewSSERelayDataStoreAdapter(baseDataStoreFactory,
		store.SSERelayDataStoreParams{
			SDKKey:            envConfig.SdkKey,
			AllPublisher:      allPublisher,
			FlagsPublisher:    flagsPublisher,
			PingPublisher:     pingPublisher,
			HeartbeatInterval: c.Main.HeartbeatIntervalSecs,
		})
	clientConfig.DataStore = storeAdapter

	var eventDispatcher *events.EventDispatcher
	if c.Events.SendEvents {
		envLoggers.Info("Proxying events for this environment")
		eventDispatcher = events.NewEventDispatcher(envConfig.SdkKey, envConfig.MobileKey, envConfig.EnvId,
			envLoggers, c.Events, httpConfig, storeAdapter)
	}

	eventsPublisher, err := events.NewHttpEventPublisher(envConfig.SdkKey, envLoggers,
		events.OptionUri(c.Events.EventsUri),
		events.OptionClient{Client: httpConfig.Client()})
	if err != nil {
		return nil, fmt.Errorf("unable to create publisher: %s", err)
	}

	em, err := metricsManager.AddEnvironment(envName, eventsPublisher)
	if err != nil {
		return nil, fmt.Errorf("unable to create metrics processor: %s", err)
	}

	clientContext := &clientContextImpl{
		name:         envName,
		envId:        envConfig.EnvId,
		sdkKey:       envConfig.SdkKey,
		mobileKey:    envConfig.MobileKey,
		storeAdapter: storeAdapter,
		loggers:      envLoggers,
		metricsEnv:   em,
		ttl:          time.Minute * time.Duration(envConfig.TtlMinutes),
		handlers: clientHandlers{
			eventDispatcher:    eventDispatcher,
			allStreamHandler:   allPublisher.Handler(envConfig.SdkKey),
			flagsStreamHandler: flagsPublisher.Handler(envConfig.SdkKey),
			pingStreamHandler:  pingPublisher.Handler(envConfig.SdkKey),
		},
	}

	// Connecting may take time, so do this in parallel
	go func(envName string, envConfig config.EnvConfig) {
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

			loggers.Errorf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
		} else {
			loggers.Infof("Initialized LaunchDarkly client for %s\n", envName)
		}
		if readyCh != nil {
			readyCh <- clientContext
		}
	}(envName, *envConfig)

	return clientContext, nil
}

func configureDataStore(
	c config.Config,
	envConfig *config.EnvConfig,
	loggers ldlog.Loggers,
) (interfaces.DataStoreFactory, error) {
	var dbFactory interfaces.DataStoreFactory

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

		builder := ldredis.DataStore().
			URL(redisURL).
			Prefix(envConfig.Prefix).
			DialOptions(dialOptions...)
		dbFactory = ldcomponents.PersistentDataStore(builder).
			CacheTime(time.Duration(c.Redis.LocalTtl) * time.Millisecond)
	}
	if useConsul {
		loggers.Infof("Using Consul feature store: %s with prefix: %s", c.Consul.Host, envConfig.Prefix)
		dbFactory = ldcomponents.PersistentDataStore(
			ldconsul.DataStore().
				Address(c.Consul.Host).
				Prefix(envConfig.Prefix),
		).CacheTime(time.Duration(c.Consul.LocalTtl) * time.Millisecond)
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
		builder := lddynamodb.DataStore(tableName).
			Prefix(envConfig.Prefix)
		if c.DynamoDB.Url != "" {
			awsOptions := session.Options{
				Config: aws.Config{
					Endpoint: aws.String(c.DynamoDB.Url),
				},
			}
			builder.SessionOptions(awsOptions)
		}
		dbFactory = ldcomponents.PersistentDataStore(builder).
			CacheTime(time.Duration(c.Consul.LocalTtl) * time.Millisecond)
	}

	if dbFactory != nil {
		return dbFactory, nil
	}
	return ldcomponents.InMemoryDataStore(), nil
}
