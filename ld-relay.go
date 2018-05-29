package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/kardianos/minwinsvc"
	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/gcfg"
	ld "gopkg.in/launchdarkly/go-client.v4"
	ldr "gopkg.in/launchdarkly/go-client.v4/redis"
)

const (
	defaultRedisLocalTtlMs       = 30000
	defaultPort                  = 8030
	defaultAllowedOrigin         = "*"
	defaultEventCapacity         = 1000
	defaultEventsUri             = "https://events.launchdarkly.com/api/events"
	defaultBaseUri               = "https://app.launchdarkly.com/"
	defaultStreamUri             = "https://stream.launchdarkly.com/"
	defaultHeartbeatIntervalSecs = 180
)

var (
	Version           = "4.1.1"
	Debug             *log.Logger
	Info              *log.Logger
	Warning           *log.Logger
	Error             *log.Logger
	uuidHeaderPattern = regexp.MustCompile(`^(?:api_key )?((?:[a-z]{3}-)?[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89aAbB][a-f0-9]{3}-[a-f0-9]{12})$`)
	configFile        string
)

type EnvConfig struct {
	SdkKey        string
	ApiKey        string // deprecated, equivalent to SdkKey
	MobileKey     *string
	EnvId         *string
	Prefix        string
	AllowedOrigin *[]string
}

type Config struct {
	Main struct {
		ExitOnError            bool
		IgnoreConnectionErrors bool
		StreamUri              string
		BaseUri                string
		Port                   int
		HeartbeatIntervalSecs  int
	}
	Events struct {
		EventsUri         string
		SendEvents        bool
		FlushIntervalSecs int
		SamplingInterval  int32
		Capacity          int
		InlineUsers       bool
	}
	Redis struct {
		Host     string
		Port     int
		LocalTtl *int
	}
	Environment map[string]*EnvConfig
}

type EnvironmentStatus struct {
	SdkKey    string `json:"sdkKey"`
	EnvId     string `json:"envId,omitempty"`
	MobileKey string `json:"mobileKey,omitempty"`
	Status    string `json:"status"`
}

type ErrorJson struct {
	Message string `json:"message"`
}

type corsContext interface {
	AllowedOrigins() []string
}

type ldClientContext interface {
	Initialized() bool
}

type clientHandlers struct {
	flagsStreamHandler http.Handler
	allStreamHandler   http.Handler
	pingStreamHandler  http.Handler
	eventsHandler      http.Handler
}

type clientContext interface {
	getClient() ldClientContext
	setClient(ldClientContext)
	getStore() ld.FeatureStore
	getLogger() ld.Logger
	getHandlers() clientHandlers
}

type clientContextImpl struct {
	mu        sync.RWMutex
	client    ldClientContext
	store     ld.FeatureStore
	logger    ld.Logger
	handlers  clientHandlers
	sdkKey    string
	envId     *string
	mobileKey *string
	name      string
}

type relay struct {
	sdkClientMux    ClientMux
	mobileClientMux ClientMux
	clientSideMux   ClientSideMux
}

type EvalXResult struct {
	Value                interface{} `json:"value"`
	Variation            *int        `json:"variation,omitempty"`
	Version              int         `json:"version"`
	DebugEventsUntilDate *uint64     `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool        `json:"trackEvents"`
}

func (c *clientContextImpl) getClient() ldClientContext {
	c.mu.RLock()
	defer c.mu.RLock()
	return c.client
}

func (c *clientContextImpl) setClient(client ldClientContext) {
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

func main() {

	flag.StringVar(&configFile, "config", "/etc/ld-relay.conf", "configuration file location")

	flag.Parse()

	initLogging(ioutil.Discard, os.Stdout, os.Stdout, os.Stderr)

	var c Config
	c.Events.Capacity = defaultEventCapacity
	c.Events.EventsUri = defaultEventsUri
	c.Main.BaseUri = defaultBaseUri
	c.Main.StreamUri = defaultStreamUri
	c.Main.HeartbeatIntervalSecs = defaultHeartbeatIntervalSecs

	Info.Printf("Starting LaunchDarkly relay version %s with configuration file %s\n", formatVersion(Version), configFile)

	err := gcfg.ReadFileInto(&c, configFile)

	if err != nil {
		Error.Println("Failed to read configuration file. Exiting.")
		os.Exit(1)
	}

	if c.Redis.LocalTtl == nil {
		localTtl := defaultRedisLocalTtlMs
		c.Redis.LocalTtl = &localTtl
	}

	if c.Main.Port == 0 {
		Info.Printf("No port specified in configuration file. Using default port %d.", defaultPort)
		c.Main.Port = defaultPort
	}

	if len(c.Environment) == 0 {
		Error.Println("You must specify at least one environment in your configuration file. Exiting.")
		os.Exit(1)
	}

	relay := newRelay(c, defaultClientFactory).getHandler()

	Info.Printf("Listening on port %d\n", c.Main.Port)

	err = http.ListenAndServe(fmt.Sprintf(":%d", c.Main.Port), relay)
	if err != nil {
		if c.Main.ExitOnError {
			Error.Fatalf("Error starting http listener on port: %d  %s", c.Main.Port, err.Error())
		}
		Error.Printf("Error starting http listener on port: %d  %s", c.Main.Port, err.Error())
	}
}

func defaultClientFactory(sdkKey string, config ld.Config) (ldClientContext, error) {
	return ld.MakeCustomClient(sdkKey, config, time.Second*10)
}

func newRelay(c Config, clientFactory func(sdkKey string, config ld.Config) (ldClientContext, error)) *relay {
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
	clientSideMux := ClientSideMux{baseUri: c.Main.BaseUri, contextByKey: map[string]*clientSideContext{}}
	for key, envConfig := range c.Environment {
		if envConfig.ApiKey != "" {
			if envConfig.SdkKey == "" {
				envConfig.SdkKey = envConfig.ApiKey
				c.Environment[key] = envConfig
				Warning.Println(`"apiKey" is deprecated, please use "sdkKey"`)
			} else {
				Warning.Println(`"apiKey" and "sdkKey" were both specified; "apiKey" is deprecated, will use "sdkKey" value`)
			}
		}
		clients[envConfig.SdkKey] = nil
	}
	for envName, envConfig := range c.Environment {
		var baseFeatureStore ld.FeatureStore
		if c.Redis.Host != "" && c.Redis.Port != 0 {
			Info.Printf("Using Redis Feature Store: %s:%d with prefix: %s", c.Redis.Host, c.Redis.Port, envConfig.Prefix)
			baseFeatureStore = ldr.NewRedisFeatureStore(c.Redis.Host, c.Redis.Port, envConfig.Prefix, time.Duration(*c.Redis.LocalTtl)*time.Millisecond, Info)
		} else {
			baseFeatureStore = ld.NewInMemoryFeatureStore(Info)
		}

		logger := log.New(os.Stderr, fmt.Sprintf("[LaunchDarkly Relay (SdkKey ending with %s)] ", last5(envConfig.SdkKey)), log.LstdFlags)

		clientConfig := ld.DefaultConfig
		clientConfig.Stream = true
		clientConfig.FeatureStore = NewSSERelayFeatureStore(envConfig.SdkKey, allPublisher, flagsPublisher, pingPublisher, baseFeatureStore, c.Main.HeartbeatIntervalSecs)
		clientConfig.StreamUri = c.Main.StreamUri
		clientConfig.BaseUri = c.Main.BaseUri
		clientConfig.Logger = logger
		clientConfig.UserAgent = "LDRelay/" + Version

		clientContext := &clientContextImpl{
			name:      envName,
			envId:     envConfig.EnvId,
			sdkKey:    envConfig.SdkKey,
			mobileKey: envConfig.MobileKey,
			store:     baseFeatureStore,
			logger:    logger,
			handlers: clientHandlers{
				allStreamHandler:   allPublisher.Handler(envConfig.SdkKey),
				flagsStreamHandler: flagsPublisher.Handler(envConfig.SdkKey),
				pingStreamHandler:  pingPublisher.Handler(envConfig.SdkKey),
			},
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
			clientSideMux.contextByKey[*envConfig.EnvId] = &clientSideContext{clientContext: clientContext, allowedOrigins: allowedOrigins}
		}

		if c.Events.SendEvents {
			Info.Printf("Proxying events for environment %s", envName)
			clientContext.handlers.eventsHandler = newEventRelayHandler(envConfig.SdkKey, c, baseFeatureStore)
		}

		// Connecting may take time, so do this in parallel
		go func(envName string, envConfig EnvConfig) {
			client, err := clientFactory(envConfig.SdkKey, clientConfig)
			clientContext.setClient(client)

			if err != nil {
				if !c.Main.IgnoreConnectionErrors {
					Error.Printf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

					if c.Main.ExitOnError {
						os.Exit(1)
					}
					return
				}

				Error.Printf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
			} else {
				Info.Printf("Initialized LaunchDarkly client for %s\n", envName)
			}
		}(envName, *envConfig)
	}

	r := relay{
		sdkClientMux:    ClientMux{clientContextByKey: clients},
		mobileClientMux: ClientMux{clientContextByKey: mobileClients},
		clientSideMux:   clientSideMux,
	}
	return &r
}

func (r *relay) getHandler() http.Handler {
	router := mux.NewRouter()
	router.HandleFunc("/status", r.sdkClientMux.getStatus).Methods("GET")

	// Client-side evaluation
	clientSideMiddlewareStack := chainMiddleware(corsMiddleware, r.clientSideMux.selectClientByUrlParam)

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

	serverSideSdkRouter := router.PathPrefix("/sdk/").Subrouter()
	serverSideSdkRouter.Use(r.sdkClientMux.selectClientByAuthorizationKey)

	serverSideEvalRouter := serverSideSdkRouter.PathPrefix("/eval/").Subrouter()
	serverSideEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET")
	serverSideEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT")

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx/").Subrouter()
	serverSideEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET")
	serverSideEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT")

	// Mobile evaluation
	msdkRouter := router.PathPrefix("/msdk/").Subrouter()
	msdkRouter.Use(r.mobileClientMux.selectClientByAuthorizationKey)

	msdkEvalRouter := msdkRouter.PathPrefix("/eval/").Subrouter()
	msdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT")

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx/").Subrouter()
	msdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT")

	router.Handle("/mping", r.mobileClientMux.selectClientByAuthorizationKey(http.HandlerFunc(pingStreamHandler))).Methods("GET")

	clientSidePingRouter := router.PathPrefix("/ping/{envId}").Subrouter()
	clientSidePingRouter.Use(clientSideMiddlewareStack)
	clientSidePingRouter.Use(mux.CORSMethodMiddleware(clientSidePingRouter))
	clientSidePingRouter.HandleFunc("", pingStreamHandler).Methods("GET", "OPTIONS")

	clientSideStreamEvalRouter := router.PathPrefix("/eval/{envId}").Subrouter()
	clientSideStreamEvalRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideStreamEvalRouter))
	// For now we implement eval as simply ping
	clientSideStreamEvalRouter.HandleFunc("/{user}", pingStreamHandler).Methods("GET", "OPTIONS")
	clientSideStreamEvalRouter.HandleFunc("", pingStreamHandler).Methods("REPORT", "OPTIONS")

	mobileEventsRouter := router.PathPrefix("/mobile").Subrouter()
	mobileEventsRouter.Use(r.mobileClientMux.selectClientByAuthorizationKey)
	mobileEventsRouter.HandleFunc("/events/bulk", bulkEventHandler).Methods("POST")
	mobileEventsRouter.HandleFunc("/events", bulkEventHandler).Methods("POST")
	mobileEventsRouter.HandleFunc("", bulkEventHandler).Methods("POST")

	clientSideBulkEventsRouter := router.PathPrefix("/events/bulk/{envId}").Subrouter()
	clientSideBulkEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideBulkEventsRouter))
	clientSideBulkEventsRouter.HandleFunc("", bulkEventHandler).Methods("POST", "OPTIONS")

	clientSideImageEventsRouter := router.PathPrefix("/a/{envId}.gif").Subrouter()
	clientSideImageEventsRouter.Use(clientSideMiddlewareStack, mux.CORSMethodMiddleware(clientSideImageEventsRouter))
	clientSideImageEventsRouter.HandleFunc("", getEventsImage).Methods("GET", "OPTIONS")

	serverSideRouter := router.PathPrefix("").Subrouter()
	serverSideRouter.Use(r.sdkClientMux.selectClientByAuthorizationKey)
	serverSideRouter.HandleFunc("/all", allStreamHandler).Methods("GET")
	serverSideRouter.HandleFunc("/flags", flagsStreamHandler).Methods("GET")
	serverSideRouter.HandleFunc("/bulk", bulkEventHandler).Methods("POST")

	return router
}

type ClientMux struct {
	clientContextByKey map[string]*clientContextImpl
}

func (m ClientMux) getStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	envs := make(map[string]EnvironmentStatus)

	healthy := true
	for _, clientCtx := range m.clientContextByKey {
		var status EnvironmentStatus
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

	resp := make(map[string]interface{})

	resp["environments"] = envs
	if healthy {
		resp["status"] = "healthy"
	} else {
		resp["status"] = "degraded"
	}

	data, _ := json.Marshal(resp)

	w.Write(data)
}

func (m ClientMux) selectClientByAuthorizationKey(next http.Handler) http.Handler {
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

		req = req.WithContext(context.WithValue(req.Context(), "context", clientCtx))
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
		w.Write(ErrorJsonMsg(userDecodeErr.Error()))
		return
	}

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
			w.Write(ErrorJsonMsg("Service not initialized"))
			return
		}
	}

	if user.Key == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(ErrorJsonMsg("User must have a 'key' attribute"))
		return
	}

	items, err := store.All(ld.Features)
	if err != nil {
		logger.Printf("WARN: Unable to fetch flags from feature store. Returning nil map. Error: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write(ErrorJsonMsgf("Error fetching flags from feature store: %s", err))
		return
	}

	response := make(map[string]interface{}, len(items))
	for _, item := range items {
		if flag, ok := item.(*ld.FeatureFlag); ok {
			value, variation, _ := flag.Evaluate(*user, store)
			var result interface{}
			if valueOnly {
				result = value
			} else {
				result = EvalXResult{
					Value:                value,
					Variation:            variation,
					Version:              flag.Version,
					TrackEvents:          flag.TrackEvents,
					DebugEventsUntilDate: flag.DebugEventsUntilDate,
				}
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

func bulkEventHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)
	if clientCtx.getHandlers().eventsHandler == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(ErrorJsonMsg("Event proxy is not enabled for this environment"))
		return
	}
	clientCtx.getHandlers().eventsHandler.ServeHTTP(w, req)
}

func ErrorJsonMsg(msg string) (j []byte) {
	j, _ = json.Marshal(ErrorJson{msg})
	return
}

func ErrorJsonMsgf(fmtStr string, args ...interface{}) []byte {
	return ErrorJsonMsg(fmt.Sprintf(fmtStr, args...))
}

// Decodes a base64-encoded go-client v2 user.
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

func formatVersion(version string) string {
	split := strings.Split(version, "+")

	if len(split) == 2 {
		return fmt.Sprintf("%s (build %s)", split[0], split[1])
	}
	return version
}

func initLogging(
	debugHandle io.Writer,
	infoHandle io.Writer,
	warningHandle io.Writer,
	errorHandle io.Writer) {

	Debug = log.New(debugHandle,
		"DEBUG: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Info = log.New(infoHandle,
		"INFO: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Warning = log.New(warningHandle,
		"WARNING: ",
		log.Ldate|log.Ltime|log.Lshortfile)

	Error = log.New(errorHandle,
		"ERROR: ",
		log.Ldate|log.Ltime|log.Lshortfile)
}

func last5(str string) string {
	if len(str) >= 5 {
		return str[len(str)-5:]
	}
	return str
}

func getClientContext(req *http.Request) clientContext {
	return req.Context().Value("context").(clientContext)
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
