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
	"time"

	"github.com/gorilla/mux"
	_ "github.com/kardianos/minwinsvc"
	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/gcfg"
	ld "gopkg.in/launchdarkly/go-client.v4"
	ldr "gopkg.in/launchdarkly/go-client.v4/redis"
)

const (
	defaultRedisLocalTtlMs = 30000
	defaultPort            = 8030
	defaultAllowedOrigin   = "*"
)

var (
	VERSION           = "DEV"
	Debug             *log.Logger
	Info              *log.Logger
	Warning           *log.Logger
	Error             *log.Logger
	uuidHeaderPattern = regexp.MustCompile(`^(?:api_key )?((?:[a-z]{3}-)?[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89aAbB][a-f0-9]{3}-[a-f0-9]{12})$`)
	configFile        string
)

type EnvConfig struct {
	ApiKey        string
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

type StatusEntry struct {
	Status string `json:"status"`
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

type clientContext interface {
	getClient() ldClientContext
	getStore() ld.FeatureStore
	getLogger() ld.Logger
}

type clientContextImpl struct {
	client ldClientContext
	store  ld.FeatureStore
	logger ld.Logger
}

type EvalXResult struct {
	Value                interface{} `json:"value"`
	Variation            *int        `json:"variation,omitempty"`
	Version              int         `json:"version"`
	DebugEventsUntilDate *uint64     `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool        `json:"trackEvents"`
}

func (c *clientContextImpl) getClient() ldClientContext {
	return c.client
}

func (c *clientContextImpl) getStore() ld.FeatureStore {
	return c.store
}

func (c *clientContextImpl) getLogger() ld.Logger {
	return c.logger
}

func main() {

	flag.StringVar(&configFile, "config", "/etc/ld-relay.conf", "configuration file location")

	flag.Parse()

	initLogging(ioutil.Discard, os.Stdout, os.Stdout, os.Stderr)

	var c Config

	Info.Printf("Starting LaunchDarkly relay version %s with configuration file %s\n", formatVersion(VERSION), configFile)

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

	allPublisher := eventsource.NewServer()
	allPublisher.Gzip = false
	allPublisher.AllowCORS = true
	allPublisher.ReplayAll = true
	flagsPublisher := eventsource.NewServer()
	flagsPublisher.Gzip = false
	flagsPublisher.AllowCORS = true
	flagsPublisher.ReplayAll = true

	clients := map[string]clientContext{}
	mobileClients := map[string]clientContext{}
	clientSideMux := ClientSideMux{baseUri: c.Main.BaseUri, contextByKey: map[string]*ClientSideContext{}}

	allStreamHandlers := map[string]http.Handler{}
	flagsStreamHandlers := map[string]http.Handler{}
	eventHandlers := map[string]http.Handler{}

	for _, envConfig := range c.Environment {
		clients[envConfig.ApiKey] = nil
	}

	for envName, envConfig := range c.Environment {
		go func(envName string, envConfig EnvConfig) {
			var baseFeatureStore ld.FeatureStore
			if c.Redis.Host != "" && c.Redis.Port != 0 {
				Info.Printf("Using Redis Feature Store: %s:%d with prefix: %s", c.Redis.Host, c.Redis.Port, envConfig.Prefix)
				baseFeatureStore = ldr.NewRedisFeatureStore(c.Redis.Host, c.Redis.Port, envConfig.Prefix, time.Duration(*c.Redis.LocalTtl)*time.Millisecond, Info)
			} else {
				baseFeatureStore = ld.NewInMemoryFeatureStore(Info)
			}

			logger := log.New(os.Stderr, fmt.Sprintf("[LaunchDarkly Relay (ApiKey ending with %s)] ", last5(envConfig.ApiKey)), log.LstdFlags)

			clientConfig := ld.DefaultConfig
			clientConfig.Stream = true
			clientConfig.FeatureStore = NewSSERelayFeatureStore(envConfig.ApiKey, allPublisher, flagsPublisher, baseFeatureStore, c.Main.HeartbeatIntervalSecs)
			clientConfig.StreamUri = c.Main.StreamUri
			clientConfig.BaseUri = c.Main.BaseUri
			clientConfig.Logger = logger

			client, err := ld.MakeCustomClient(envConfig.ApiKey, clientConfig, time.Second*10)

			clientContext := &clientContextImpl{
				client: client,
				store:  baseFeatureStore,
				logger: logger,
			}

			clients[envConfig.ApiKey] = clientContext
			if envConfig.MobileKey != nil && *envConfig.MobileKey != "" {
				mobileClients[*envConfig.MobileKey] = clientContext
			}

			if envConfig.EnvId != nil && *envConfig.EnvId != "" {
				var allowedOrigins []string
				if envConfig.AllowedOrigin != nil && len(*envConfig.AllowedOrigin) != 0 {
					allowedOrigins = *envConfig.AllowedOrigin
				}
				clientSideMux.contextByKey[*envConfig.EnvId] = &ClientSideContext{clientContext: clientContext, allowedOrigins: allowedOrigins}
			}

			if err != nil && !c.Main.IgnoreConnectionErrors {
				Error.Printf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

				if c.Main.ExitOnError {
					os.Exit(1)
				}
			} else {
				if err != nil {
					Error.Printf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
				} else {
					Info.Printf("Initialized LaunchDarkly client for %s\n", envName)
				}
				// create a handler from the publisher for this environment
				allHandler := allPublisher.Handler(envConfig.ApiKey)
				flagsHandler := flagsPublisher.Handler(envConfig.ApiKey)
				allStreamHandlers[envConfig.ApiKey] = allHandler
				flagsStreamHandlers[envConfig.ApiKey] = flagsHandler

				if c.Events.SendEvents {
					Info.Printf("Proxying events for environment %s", envName)
					eventHandler := newRelayHandler(envConfig.ApiKey, c, baseFeatureStore)
					eventHandlers[envConfig.ApiKey] = eventHandler
				}
			}
		}(envName, *envConfig)
	}

	router := mux.NewRouter()

	bulkEventHandler := SdkHandlerMux{handlersByKey: eventHandlers}
	allStreamHandler := SdkHandlerMux{handlersByKey: allStreamHandlers}
	flagsStreamHandler := SdkHandlerMux{handlersByKey: flagsStreamHandlers}

	sdkClientMux := ClientMux{clientContextByKey: clients}
	mobileClientMux := ClientMux{clientContextByKey: mobileClients}

	router.Handle("/bulk", bulkEventHandler).Methods("POST")
	router.Handle("/all", allStreamHandler).Methods("GET")
	router.Handle("/flags", flagsStreamHandler).Methods("GET")
	router.HandleFunc("/status", sdkClientMux.getStatus).Methods("GET")

	serverSideSdkRouter := router.PathPrefix("/sdk").Subrouter()
	serverSideSdkRouter.Use(sdkClientMux.selectClientByAuthorizationKey)

	serverSideEvalRouter := serverSideSdkRouter.PathPrefix("/eval").Subrouter()
	serverSideEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET")
	serverSideEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT")

	serverSideEvalXRouter := serverSideSdkRouter.PathPrefix("/evalx").Subrouter()
	serverSideEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET")
	serverSideEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT")

	// Client-side evaluation
	clientSideSdkRouter := router.PathPrefix("/sdk").Subrouter()
	clientSideSdkRouter.Use(corsMiddleware)

	clientSideEvalRouter := clientSideSdkRouter.PathPrefix("/eval/{envId}").Subrouter()
	clientSideEvalRouter.Use(clientSideMux.selectClientByUrlParam)
	clientSideEvalRouter.Handle("/users/{user}", allowMethodOptionsHandler("GET")).Methods("OPTIONS")
	clientSideEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET")
	clientSideEvalRouter.Handle("/user", allowMethodOptionsHandler("REPORT")).Methods("OPTIONS")
	clientSideEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT")

	clientSideEvalXRouter := clientSideSdkRouter.PathPrefix("/evalx/{envId}").Subrouter()
	clientSideEvalXRouter.Use(clientSideMux.selectClientByUrlParam)
	clientSideEvalXRouter.Handle("/users/{user}", allowMethodOptionsHandler("GET")).Methods("OPTIONS")
	clientSideEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET")
	clientSideEvalXRouter.Handle("/user", allowMethodOptionsHandler("REPORT")).Methods("OPTIONS")
	clientSideEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT")

	goalsRouter := clientSideSdkRouter.PathPrefix("/goals/{envId}").Subrouter()
	goalsRouter.Use(clientSideMux.selectClientByUrlParam)
	goalsRouter.Handle("", allowMethodOptionsHandler("GET")).Methods("OPTIONS")
	goalsRouter.HandleFunc("", clientSideMux.getGoals).Methods("GET")

	// Mobile evaluation
	msdkRouter := router.PathPrefix("/msdk").Subrouter()
	msdkRouter.Use(mobileClientMux.selectClientByAuthorizationKey)

	msdkEvalXRouter := msdkRouter.PathPrefix("/evalx").Subrouter()
	msdkEvalXRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlags).Methods("GET")
	msdkEvalXRouter.HandleFunc("/user", evaluateAllFeatureFlags).Methods("REPORT")

	msdkEvalRouter := msdkRouter.PathPrefix("/eval").Subrouter()
	msdkEvalRouter.HandleFunc("/users/{user}", evaluateAllFeatureFlagsValueOnly).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", evaluateAllFeatureFlagsValueOnly).Methods("REPORT")

	Info.Printf("Listening on port %d\n", c.Main.Port)

	err = http.ListenAndServe(fmt.Sprintf(":%d", c.Main.Port), router)
	if err != nil {
		if c.Main.ExitOnError {
			Error.Fatalf("Error starting http listener on port: %d  %s", c.Main.Port, err.Error())
		}
		Error.Printf("Error starting http listener on port: %d  %s", c.Main.Port, err.Error())
	}
}

type ClientMux struct {
	clientContextByKey map[string]clientContext
}

type SdkHandlerMux struct {
	handlersByKey map[string]http.Handler
}

func (m ClientMux) getStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	envs := make(map[string]StatusEntry)

	healthy := true
	for k, clientCtx := range m.clientContextByKey {
		client := clientCtx.getClient()
		if client == nil || !client.Initialized() {
			envs[k] = StatusEntry{Status: "disconnected"}
			healthy = false
		} else {
			envs[k] = StatusEntry{Status: "connected"}
		}
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

		req = req.WithContext(context.WithValue(req.Context(), "context", clientCtx))
		next.ServeHTTP(w, req)
	})
}

func (m SdkHandlerMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	authKey, err := fetchAuthToken(req)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	handler := m.handlersByKey[authKey]

	if handler == nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("ld-relay is not configured for the provided key"))
		return
	}

	handler.ServeHTTP(w, req)
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
		defer req.Body.Close()
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

	clientCtx := req.Context().Value("context").(clientContext)
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
