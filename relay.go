package relay

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/gcfg"
	ld "gopkg.in/launchdarkly/go-client.v4"
	"gopkg.in/launchdarkly/go-client.v4/ldconsul"
	"gopkg.in/launchdarkly/go-client.v4/lddynamodb"
	ldr "gopkg.in/launchdarkly/go-client.v4/redis"

	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/metrics"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/store"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/util"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/version"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

const (
	defaultRedisLocalTtlMs       = 30000
	defaultPort                  = 8030
	defaultAllowedOrigin         = "*"
	defaultEventCapacity         = 1000
	defaultEventsUri             = "https://events.launchdarkly.com"
	defaultBaseUri               = "https://app.launchdarkly.com"
	defaultStreamUri             = "https://stream.launchdarkly.com"
	defaultHeartbeatIntervalSecs = 180
	defaultMetricsPrefix         = "launchdarkly_relay"
	defaultFlushIntervalSecs     = 5

	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"
)

var (
	uuidHeaderPattern = regexp.MustCompile(`^(?:api_key )?((?:[a-z]{3}-)?[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89aAbB][a-f0-9]{3}-[a-f0-9]{12})$`)
)

// EnvConfig describes an environment to be relayed
type EnvConfig struct {
	SdkKey             string
	ApiKey             string // deprecated, equivalent to SdkKey
	MobileKey          *string
	EnvId              *string
	Prefix             string // used only if Redis, Consul, or DynamoDB is enabled
	TableName          string // used only if DynamoDB is enabled
	AllowedOrigin      *[]string
	InsecureSkipVerify bool
}

type ExporterConfig interface {
	toOptions() metrics.ExporterOptions
	enabled() bool
}

type DatadogConfig struct {
	TraceAddr *string
	StatsAddr *string
	Tag       []string
	CommonMetricsConfig
}

func (c DatadogConfig) toOptions() metrics.ExporterOptions {
	return metrics.DatadogOptions{
		TraceAddr: c.TraceAddr,
		StatsAddr: c.StatsAddr,
		Tags:      c.Tag,
		Prefix:    c.getPrefix(),
	}
}

type StackdriverConfig struct {
	ProjectID string
	CommonMetricsConfig
}

func (c StackdriverConfig) toOptions() metrics.ExporterOptions {
	return metrics.StackdriverOptions{
		ProjectID: c.ProjectID,
		Prefix:    c.getPrefix(),
	}
}

type PrometheusConfig struct {
	Port int
	CommonMetricsConfig
}

func (c PrometheusConfig) toOptions() (options metrics.ExporterOptions) {
	return metrics.PrometheusOptions{
		Port:   c.Port,
		Prefix: c.getPrefix(),
	}
}

type CommonMetricsConfig struct {
	Enabled bool
	Prefix  string
}

func (c CommonMetricsConfig) getPrefix() string {
	prefix := c.Prefix
	if prefix == "" {
		return defaultMetricsPrefix
	}
	return prefix
}
func (c CommonMetricsConfig) enabled() bool {
	return c.Enabled
}

// Config describes the configuration for a relay instance
type Config struct {
	Main struct {
		ExitOnError            bool
		IgnoreConnectionErrors bool
		StreamUri              string
		BaseUri                string
		Port                   int
		HeartbeatIntervalSecs  int
	}
	Events events.Config
	Redis  struct {
		Host     string
		Port     int
		Url      string
		LocalTtl int
	}
	Consul struct {
		Host     string
		LocalTtl int
	}
	DynamoDB struct {
		Enabled   bool
		TableName string
		LocalTtl  int
	}
	Environment map[string]*EnvConfig
	MetricsConfig
}

type MetricsConfig struct {
	Datadog     DatadogConfig
	Stackdriver StackdriverConfig
	Prometheus  PrometheusConfig
}

func (c MetricsConfig) toOptions() (options []metrics.ExporterOptions) {
	exporterConfigs := []ExporterConfig{c.Datadog, c.Stackdriver, c.Prometheus}
	for _, e := range exporterConfigs {
		if e.enabled() {
			options = append(options, e.toOptions())
		}
	}
	return options
}

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

var DefaultConfig = Config{
	Events: events.Config{
		Capacity:  defaultEventCapacity,
		EventsUri: defaultEventsUri,
	},
}

func init() {
	DefaultConfig.Main.BaseUri = defaultBaseUri
	DefaultConfig.Main.StreamUri = defaultStreamUri
	DefaultConfig.Main.HeartbeatIntervalSecs = defaultHeartbeatIntervalSecs
	DefaultConfig.Main.Port = defaultPort
	DefaultConfig.Redis.LocalTtl = defaultRedisLocalTtlMs
	DefaultConfig.Events.FlushIntervalSecs = defaultFlushIntervalSecs
}

// LoadConfigFile reads a config file into a Config struct
func LoadConfigFile(c *Config, path string) error {
	if err := gcfg.ReadFileInto(c, path); err != nil {
		return fmt.Errorf(`failed to read configuration file "%s": %s`, path, err)
	}

	for envName, envConfig := range c.Environment {
		if envConfig.ApiKey != "" {
			if envConfig.SdkKey == "" {
				envConfig.SdkKey = envConfig.ApiKey
				c.Environment[envName] = envConfig
				logging.Warning.Println(`"apiKey" is deprecated, please use "sdkKey"`)
			} else {
				logging.Warning.Println(`"apiKey" and "sdkKey" were both specified; "apiKey" is deprecated, will use "sdkKey" value`)
			}
		}
	}

	return nil
}

type corsContext interface {
	AllowedOrigins() []string
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
	getLogger() ld.Logger
	getHandlers() clientHandlers
	getMetricsCtx() context.Context
}

type clientContextImpl struct {
	mu         sync.RWMutex
	client     LdClientContext
	store      ld.FeatureStore
	logger     ld.Logger
	handlers   clientHandlers
	sdkKey     string
	envId      *string
	mobileKey  *string
	name       string
	metricsCtx context.Context
}

// Relay relays endpoints to and from the LaunchDarkly service
type Relay struct {
	http.Handler
	sdkClientMux    clientMux
	mobileClientMux clientMux
	clientSideMux   clientSideMux
}

type evalXResult struct {
	Value                interface{}                   `json:"value"`
	Variation            *int                          `json:"variation,omitempty"`
	Version              int                           `json:"version"`
	DebugEventsUntilDate *uint64                       `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool                          `json:"trackEvents"`
	Reason               *ld.EvaluationReasonContainer `json:"reason,omitempty"`
}

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

func (c *clientContextImpl) getLogger() ld.Logger {
	return c.logger
}

func (c *clientContextImpl) getHandlers() clientHandlers {
	return c.handlers
}

func (c *clientContextImpl) getMetricsCtx() context.Context {
	return c.metricsCtx
}

type clientFactoryFunc func(sdkKey string, config ld.Config) (LdClientContext, error)

// DefaultClientFactory creates a default client for connecting to the LaunchDarkly stream
func DefaultClientFactory(sdkKey string, config ld.Config) (LdClientContext, error) {
	return ld.MakeCustomClient(sdkKey, config, time.Second*10)
}

// NewRelay creates a new relay given a configuration and a method to create a client
func NewRelay(c Config, clientFactory clientFactoryFunc) (*Relay, error) {
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
		return nil, fmt.Errorf("you must specify at least one environment in your configuration file")
	}

	baseUrl, err := url.Parse(c.Main.BaseUri)
	if err != nil {
		return nil, fmt.Errorf(`unable to parse baseUri "%s"`, c.Main.BaseUri)
	}

	for envName, envConfig := range c.Environment {
		clientContext, err := newClientContext(envName, envConfig, c, clientFactory, allPublisher, flagsPublisher, pingPublisher)
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
	r.Handler = r.makeHandler()
	return &r, nil
}

func (r *Relay) makeHandler() http.Handler {
	router := mux.NewRouter()
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
	clientSideSdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET", "OPTIONS")
	clientSideSdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT", "OPTIONS")

	clientSideSdkEvalXRouter := router.PathPrefix("/sdk/evalx/{envId}/").Subrouter()
	clientSideSdkEvalXRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideSdkEvalXRouter))
	clientSideSdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET", "OPTIONS")
	clientSideSdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT", "OPTIONS")

	serverSideMiddlewareStack := chainMiddleware(
		r.sdkClientMux.selectClientByAuthorizationKey,
		requestCountMiddleware(metrics.ServerRequests))

	serverSideSdkRouter := router.PathPrefix("/sdk/").Subrouter()
	// TODO: there is a bug in gorilla mux (see see https://github.com/gorilla/mux/pull/378) that means the middleware below because it will not be run if it matches any earlier prefix.  Until it is fixed, we have to apply the middleware explicitly
	// serverSideSdkRouter.Use(serverSideMiddlewareStack)

	serverSideEvalRouter := serverSideSdkRouter.PathPrefix("/eval/").Subrouter()
	serverSideEvalRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly))).Methods("GET")
	serverSideEvalRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly))).Methods("REPORT")

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.Handle("/users/{user}", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags))).Methods("GET")
	serverSideEvalXRouter.Handle("/user", serverSideMiddlewareStack(http.HandlerFunc(evaluateAllFeatureFlags))).Methods("REPORT")

	// Mobile evaluation
	mobileMiddlewareStack := chainMiddleware(
		r.mobileClientMux.selectClientByAuthorizationKey,
		requestCountMiddleware(metrics.MobileRequests))

	msdkRouter := router.PathPrefix("/msdk/").Subrouter()
	msdkRouter.Use(mobileMiddlewareStack)

	msdkEvalRouter := msdkRouter.PathPrefix("/eval/").Subrouter()
	msdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT")

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT")

	mobileStreamRouter := router.PathPrefix("/meval").Subrouter()
	mobileStreamRouter.Use(mobileMiddlewareStack)
	mobileStreamRouter.HandleFunc("", countMobileConns(pingStreamHandler)).Methods("REPORT")
	mobileStreamRouter.HandleFunc("/{user}", countMobileConns(pingStreamHandler)).Methods("GET")

	router.Handle("/mping", r.mobileClientMux.selectClientByAuthorizationKey(countMobileConns(pingStreamHandler))).Methods("GET")

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSidePingRouter))
	clientSidePingRouter.HandleFunc("", countBrowserConns(pingStreamHandler)).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideStreamEvalRouter))
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.HandleFunc("/{user}", countBrowserConns(pingStreamHandler)).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.HandleFunc("", countBrowserConns(pingStreamHandler)).Methods("REPORT", "OPTIONS")

	mobileEventsRouter := router.PathPrefix("/mobile").Subrouter()
	mobileEventsRouter.Use(mobileMiddlewareStack)
	mobileEventsRouter.HandleFunc("/events/bulk", bulkEventHandler(events.MobileSDKEventsEndpoint)).Methods("POST")
	mobileEventsRouter.HandleFunc("/events", bulkEventHandler(events.MobileSDKEventsEndpoint)).Methods("POST")
	mobileEventsRouter.HandleFunc("", bulkEventHandler(events.MobileSDKEventsEndpoint)).Methods("POST")

	clientSideBulkEventsRouter := router.PathPrefix("/events/bulk/{envId}").Subrouter()
	clientSideBulkEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideBulkEventsRouter))
	clientSideBulkEventsRouter.HandleFunc("", bulkEventHandler(events.JavaScriptSDKEventsEndpoint)).Methods("POST", "OPTIONS")

	clientSideImageEventsRouter := router.PathPrefix("/a/{envId}.gif").Subrouter()
	clientSideImageEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideImageEventsRouter))
	clientSideImageEventsRouter.HandleFunc("", getEventsImage).Methods("GET", "OPTIONS")

	serverSideRouter := router.PathPrefix("").Subrouter()
	serverSideRouter.Use(serverSideMiddlewareStack)
	serverSideRouter.HandleFunc("/all", countServerConns(allStreamHandler)).Methods("GET")
	serverSideRouter.HandleFunc("/flags", countServerConns(flagsStreamHandler)).Methods("GET")
	serverSideRouter.HandleFunc("/bulk", bulkEventHandler(events.ServerSDKEventsEndpoint)).Methods("POST")

	return router
}

func newClientContext(envName string, envConfig *EnvConfig, c Config, clientFactory clientFactoryFunc, allPublisher, flagsPublisher, pingPublisher *eventsource.Server) (*clientContextImpl, error) {
	baseFeatureStore, err := createFeatureStore(c, envConfig)
	if err != nil {
		return nil, err
	}
	logger := log.New(os.Stderr, fmt.Sprintf("[LaunchDarkly Relay (SdkKey ending with %s)] ", last5(envConfig.SdkKey)), log.LstdFlags)

	clientConfig := ld.DefaultConfig
	clientConfig.Stream = true
	clientConfig.FeatureStore = store.NewSSERelayFeatureStore(envConfig.SdkKey, allPublisher, flagsPublisher, pingPublisher, baseFeatureStore, c.Main.HeartbeatIntervalSecs)
	clientConfig.StreamUri = c.Main.StreamUri
	clientConfig.BaseUri = c.Main.BaseUri
	clientConfig.Logger = logger
	clientConfig.UserAgent = "LDRelay/" + version.Version

	var eventDispatcher *events.EventDispatcher
	if c.Events.SendEvents {
		logging.Info.Printf("Proxying events for environment %s", envName)
		eventDispatcher = events.NewEventDispatcher(envConfig.SdkKey, envConfig.MobileKey, envConfig.EnvId, c.Events, baseFeatureStore)
	}

	eventsPublisher, err := events.NewHttpEventPublisher(envConfig.SdkKey, events.OptionUri(c.Events.EventsUri))
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
		logger:     logger,
		metricsCtx: m.OpenCensusCtx,
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
			if !c.Main.IgnoreConnectionErrors {
				logging.Error.Printf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

				if c.Main.ExitOnError {
					os.Exit(1)
				}
				return
			}

			logging.Error.Printf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
		} else {
			logging.Info.Printf("Initialized LaunchDarkly client for %s\n", envName)
		}
	}(envName, *envConfig)

	return clientContext, nil
}

func createFeatureStore(c Config, envConfig *EnvConfig) (ld.FeatureStore, error) {
	if c.Redis.Url != "" || c.Redis.Host != "" {
		var hostOption ldr.FeatureStoreOption
		if c.Redis.Url != "" {
			if c.Redis.Host != "" {
				logging.Warning.Println("Both a URL and a hostname were specified for Redis; will use the URL")
			}
			logging.Info.Printf("Using Redis feature store: %s with prefix: %s", c.Redis.Url, envConfig.Prefix)
			hostOption = ldr.URL(c.Redis.Url)
		} else {
			port := c.Redis.Port
			if port == 0 {
				port = 6379
			}
			logging.Info.Printf("Using Redis feature store: %s:%d with prefix: %s", c.Redis.Host, port, envConfig.Prefix)
			hostOption = ldr.HostAndPort(c.Redis.Host, port)
		}
		return ldr.NewRedisFeatureStoreWithDefaults(hostOption, ldr.Prefix(envConfig.Prefix),
			ldr.CacheTTL(time.Duration(c.Redis.LocalTtl)*time.Millisecond), ldr.Logger(logging.Info))
	}
	if c.Consul.Host != "" {
		logging.Info.Printf("Using Consul feature store: %s with prefix: %s", c.Consul.Host, envConfig.Prefix)
		return ldconsul.NewConsulFeatureStore(ldconsul.Address(c.Consul.Host), ldconsul.Prefix(envConfig.Prefix),
			ldconsul.CacheTTL(time.Duration(c.Consul.LocalTtl)*time.Millisecond), ldconsul.Logger(logging.Info))
	}
	if c.DynamoDB.Enabled {
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
		logging.Info.Printf("Using DynamoDB feature store: %s with prefix: %s", tableName, envConfig.Prefix)
		return lddynamodb.NewDynamoDBFeatureStore(tableName, lddynamodb.Prefix(envConfig.Prefix),
			lddynamodb.CacheTTL(time.Duration(c.DynamoDB.LocalTtl)*time.Millisecond), lddynamodb.Logger(logging.Info))
	}
	return ld.NewInMemoryFeatureStore(logging.Info), nil
}

type clientMux struct {
	clientContextByKey map[string]*clientContextImpl
}

func (m clientMux) getStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	envs := make(map[string]environmentStatus)

	healthy := true
	for _, clientCtx := range m.clientContextByKey {
		var status environmentStatus
		if clientCtx.envId != nil {
			status.EnvId = *clientCtx.envId
		}
		if clientCtx.mobileKey != nil {
			status.MobileKey = obscureKey(*clientCtx.mobileKey)
		}
		status.SdkKey = obscureKey(clientCtx.sdkKey)
		client := clientCtx.getClient()
		if client == nil || !client.Initialized() {
			status.Status = "disconnected"
			healthy = false
		} else {
			status.Status = "connected"
		}
		envs[clientCtx.name] = status
	}

	resp := struct {
		Environments  map[string]environmentStatus `json:"environments"`
		Status        string                       `json:"status"`
		Version       string                       `json:"version"`
		ClientVersion string                       `json:"clientVersion"`
	}{
		Environments:  envs,
		Version:       version.Version,
		ClientVersion: ld.Version,
	}

	if healthy {
		resp.Status = "healthy"
	} else {
		resp.Status = "degraded"
	}

	data, _ := json.Marshal(resp)

	w.Write(data)
}

func (m clientMux) selectClientByAuthorizationKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		authKey, err := fetchAuthToken(req)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		clientCtx := m.clientContextByKey[authKey]

		if clientCtx == nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("ld-relay is not configured for the provided key"))
			return
		}

		if clientCtx.getClient() == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("client was not initialized"))
			return
		}

		req = req.WithContext(context.WithValue(req.Context(), contextKey, clientCtx))
		next.ServeHTTP(w, req)
	})
}

func evaluateAllFeatureFlagsValueOnly(w http.ResponseWriter, req *http.Request) {
	evaluateAllShared(w, req, true)
}

func evaluateAllFeatureFlags(w http.ResponseWriter, req *http.Request) {
	evaluateAllShared(w, req, false)
}

func evaluateAllShared(w http.ResponseWriter, req *http.Request, valueOnly bool) {
	var user *ld.User
	var userDecodeErr error
	if req.Method == "REPORT" {
		if req.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			w.Write([]byte("Content-Type must be application/json."))
			return
		}

		body, _ := ioutil.ReadAll(req.Body)
		userDecodeErr = json.Unmarshal(body, &user)
	} else {
		base64User := mux.Vars(req)["user"]
		user, userDecodeErr = UserV2FromBase64(base64User)
	}
	if userDecodeErr != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(util.ErrorJsonMsg(userDecodeErr.Error()))
		return
	}

	withReasons := req.URL.Query().Get("withReasons") == "true"

	clientCtx := getClientContext(req)
	client := clientCtx.getClient()
	store := clientCtx.getStore()
	logger := clientCtx.getLogger()

	w.Header().Set("Content-Type", "application/json")

	if !client.Initialized() {
		if store.Initialized() {
			logger.Println("WARN: Called before client initialization; using last known values from feature store")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			logger.Println("WARN: Called before client initialization. Feature store not available")
			w.Write(util.ErrorJsonMsg("Service not initialized"))
			return
		}
	}

	if user.Key == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(util.ErrorJsonMsg("User must have a 'key' attribute"))
		return
	}

	items, err := store.All(ld.Features)
	if err != nil {
		logger.Printf("WARN: Unable to fetch flags from feature store. Returning nil map. Error: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write(util.ErrorJsonMsgf("Error fetching flags from feature store: %s", err))
		return
	}

	response := make(map[string]interface{}, len(items))
	for _, item := range items {
		if flag, ok := item.(*ld.FeatureFlag); ok {
			detail, _ := flag.EvaluateDetail(*user, store, false)
			var result interface{}
			if valueOnly {
				result = detail.Value
			} else {
				value := evalXResult{
					Value:                detail.Value,
					Variation:            detail.VariationIndex,
					Version:              flag.Version,
					TrackEvents:          flag.TrackEvents,
					DebugEventsUntilDate: flag.DebugEventsUntilDate,
				}
				if withReasons {
					value.Reason = &ld.EvaluationReasonContainer{Reason: detail.Reason}
				}
				result = value
			}
			response[flag.Key] = result
		}
	}

	result, _ := json.Marshal(response)

	w.WriteHeader(http.StatusOK)
	w.Write(result)
}

func pingStreamHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)
	clientCtx.getHandlers().pingStreamHandler.ServeHTTP(w, req)
}

func allStreamHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)
	clientCtx.getHandlers().allStreamHandler.ServeHTTP(w, req)
}

func flagsStreamHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)
	clientCtx.getHandlers().flagsStreamHandler.ServeHTTP(w, req)
}

func bulkEventHandler(endpoint events.Endpoint) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		clientCtx := getClientContext(req)
		dispatcher := clientCtx.getHandlers().eventDispatcher
		if dispatcher == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write(util.ErrorJsonMsg("Event proxy is not enabled for this environment"))
			return
		}
		handler := dispatcher.GetHandler(endpoint)
		if handler == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write(util.ErrorJsonMsg("Internal error in event proxy"))
			logging.Error.Printf("Tried to proxy events for unsupported endpoint '%s'", endpoint)
			return
		}
		handler(w, req)
	}
}

func withGauge(handler http.HandlerFunc, measure metrics.Measure) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := getClientContext(req)
		userAgent := getUserAgent(req)
		metrics.WithGauge(ctx.getMetricsCtx(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	}
}

func withCount(handler http.HandlerFunc, measure metrics.Measure) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := getClientContext(req)
		userAgent := getUserAgent(req)
		metrics.WithCount(ctx.getMetricsCtx(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	}
}

func countMobileConns(handler http.HandlerFunc) http.HandlerFunc {
	return withCount(withGauge(handler, metrics.MobileConns), metrics.NewMobileConns)
}

func countBrowserConns(handler http.HandlerFunc) http.HandlerFunc {
	return withCount(withGauge(handler, metrics.BrowserConns), metrics.NewBrowserConns)
}

func countServerConns(handler http.HandlerFunc) http.HandlerFunc {
	return withCount(withGauge(handler, metrics.ServerConns), metrics.NewServerConns)
}

func requestCountMiddleware(measure metrics.Measure) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := getClientContext(req)
			userAgent := getUserAgent(req)
			// Ignoring internal routing error that would have been ignored anyway
			route, _ := mux.CurrentRoute(req).GetPathTemplate()
			metrics.WithRouteCount(ctx.getMetricsCtx(), userAgent, route, req.Method, func() {
				next.ServeHTTP(w, req)
			}, measure)
		})
	}
}

// UserV2FromBase64 decodes a base64-encoded go-client v2 user.
// If any decoding/unmarshaling errors occur or
// the user is missing the 'key' attribute an error is returned.
func UserV2FromBase64(base64User string) (*ld.User, error) {
	var user ld.User
	idStr, decodeErr := base64urlDecode(base64User)
	if decodeErr != nil {
		return nil, errors.New("User part of url path did not decode as valid base64")
	}

	jsonErr := json.Unmarshal(idStr, &user)

	if jsonErr != nil {
		return nil, errors.New("User part of url path did not decode to valid user as json")
	}

	if user.Key == nil {
		return nil, errors.New("User must have a 'key' attribute")
	}
	return &user, nil
}

func base64urlDecode(base64String string) ([]byte, error) {
	idStr, decodeErr := base64.URLEncoding.DecodeString(base64String)

	if decodeErr != nil {
		// base64String could be unpadded
		// see https://github.com/golang/go/issues/4237#issuecomment-267792481
		idStrRaw, decodeErrRaw := base64.RawURLEncoding.DecodeString(base64String)

		if decodeErrRaw != nil {
			return nil, errors.New("String did not decode as valid base64")
		}

		return idStrRaw, nil
	}

	return idStr, nil
}

func fetchAuthToken(req *http.Request) (string, error) {
	authHdr := req.Header.Get("Authorization")
	match := uuidHeaderPattern.FindStringSubmatch(authHdr)

	// successfully matched UUID from header
	if len(match) == 2 {
		return match[1], nil
	}

	return "", errors.New("No valid token found")
}

func last5(str string) string {
	if len(str) >= 5 {
		return str[len(str)-5:]
	}
	return str
}

func getClientContext(req *http.Request) clientContext {
	return req.Context().Value(contextKey).(clientContext)
}

func chainMiddleware(middlewares ...mux.MiddlewareFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		handler := next
		for i := len(middlewares) - 1; i >= 0; i-- {
			handler = middlewares[i](handler)
		}
		return handler
	}
}

var hexdigit = regexp.MustCompile(`[a-fA-F\d]`)

func obscureKey(key string) string {
	if len(key) > 8 {
		return key[0:4] + hexdigit.ReplaceAllString(key[4:len(key)-5], "*") + key[len(key)-5:]
	}
	return key
}

// getUserAgent returns the X-LaunchDarkly-User-Agent if available, falling back to the normal "User-Agent" header
func getUserAgent(req *http.Request) string {
	if agent := req.Header.Get(ldUserAgentHeader); agent != "" {
		return agent
	}
	return req.Header.Get(userAgentHeader)
}
