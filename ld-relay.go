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
	"github.com/gregjones/httpcache"
	_ "github.com/kardianos/minwinsvc"
	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/gcfg"
	"github.com/streamrail/concurrent-map"
	ld "gopkg.in/launchdarkly/go-client.v2"
)

const (
	defaultRedisLocalTtlMs = 30000
	defaultPort            = 8030
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
	ApiKey    string
	MobileKey *string
	EnvId     *string
	Prefix    string
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

type errorJson struct {
	Message string `json:"message"`
}

type flagReader interface {
	AllFlags(user ld.User) map[string]interface{}
}

type EvalContext interface {
	getClient() interface{}
}

type evalContextImpl struct {
	client interface{}
}

func (e evalContextImpl) getClient() interface{} {
	return e.client
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

	publisher := eventsource.NewServer()
	publisher.Gzip = false
	publisher.AllowCORS = true
	publisher.ReplayAll = true

	handlers := cmap.New()

	clients := cmap.New()
	mobileClients := cmap.New()
	clientSideClients := cmap.New()

	eventHandlers := cmap.New()

	for _, envConfig := range c.Environment {
		clients.Set(envConfig.ApiKey, nil)
	}

	for envName, envConfig := range c.Environment {
		go func(envName string, envConfig EnvConfig) {
			var baseFeatureStore ld.FeatureStore
			if c.Redis.Host != "" && c.Redis.Port != 0 {
				Info.Printf("Using Redis Feature Store: %s:%d with prefix: %s", c.Redis.Host, c.Redis.Port, envConfig.Prefix)
				baseFeatureStore = ld.NewRedisFeatureStore(c.Redis.Host, c.Redis.Port, envConfig.Prefix, time.Duration(*c.Redis.LocalTtl)*time.Millisecond, Info)
			} else {
				baseFeatureStore = ld.NewInMemoryFeatureStore(Info)
			}

			clientConfig := ld.DefaultConfig
			clientConfig.Stream = true
			clientConfig.FeatureStore = NewSSERelayFeatureStore(envConfig.ApiKey, publisher, baseFeatureStore, c.Main.HeartbeatIntervalSecs)
			clientConfig.StreamUri = c.Main.StreamUri
			clientConfig.BaseUri = c.Main.BaseUri

			client, err := ld.MakeCustomClient(envConfig.ApiKey, clientConfig, time.Second*10)

			clients.Set(envConfig.ApiKey, client)
			if envConfig.MobileKey != nil && *envConfig.MobileKey != "" {
				mobileClients.Set(*envConfig.MobileKey, client)
			}
			if envConfig.EnvId != nil && *envConfig.EnvId != "" {
				clientSideClients.Set(*envConfig.EnvId, client)
			}
			if err != nil && !c.Main.IgnoreConnectionErrors {
				Error.Printf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

				if c.Main.ExitOnError {
					os.Exit(1)
				}
			} else {
				if err != nil {
					Error.Printf("Ignoring error initializing LaunchDarkly client for %s: %+v\n", envName, err)
				}
				Info.Printf("Initialized LaunchDarkly client for %s\n", envName)
				// create a handler from the publisher for this environment
				handler := publisher.Handler(envConfig.ApiKey)
				handlers.Set(envConfig.ApiKey, handler)

				if c.Events.SendEvents {
					Info.Printf("Proxying events for environment %s", envName)
					eventHandler := newRelayHandler(envConfig.ApiKey, c)
					eventHandlers.Set(envConfig.ApiKey, eventHandler)
				}
			}
		}(envName, *envConfig)
	}

	router := mux.NewRouter()

	bulkEventHandler := muxHandler{clients: eventHandlers}
	flagsHandler := muxHandler{clients: handlers}
	clientsHandler := muxHandler{clients: clients}
	mobileClientsHandler := muxHandler{clients: mobileClients}
	// Needs base uri for http requests to LaunchDarkly
	clientSideClientsHandler := muxHandler{clients: clientSideClients, baseUri: c.Main.BaseUri}

	router.HandleFunc("/bulk", bulkEventHandler.authorizeMethod(serveHandler)).Methods("POST")

	router.HandleFunc("/status", clientsHandler.getStatus).Methods("GET")

	// Now make a single handler that dispatches to the appropriate handler based on the Authorization header
	router.HandleFunc("/flags", flagsHandler.authorizeMethod(serveHandler)).Methods("GET")

	router.HandleFunc("/sdk/goals/{envId}", clientSideClientsHandler.findEnvironment(clientSideClientsHandler.getGoals)).Methods("GET")

	sdkEvalRouter := router.PathPrefix("/sdk/eval").Subrouter()

	sdkEvalRouter.HandleFunc("/users/{user}", clientsHandler.authorizeMethod(evaluateAllFeatureFlags)).Methods("GET")
	sdkEvalRouter.HandleFunc("/user", clientsHandler.authorizeMethod(evaluateAllFeatureFlags)).Methods("REPORT")

	// Client-side evaluation
	sdkEvalRouter.HandleFunc("/{envId}/users/{user}", clientSideClientsHandler.findEnvironment(evaluateAllFeatureFlags)).Methods("GET")
	sdkEvalRouter.HandleFunc("/{envId}/user", clientSideClientsHandler.findEnvironment(evaluateAllFeatureFlags)).Methods("REPORT")

	// Mobile evaluation
	msdkEvalRouter := router.PathPrefix("/msdk/eval").Subrouter()

	msdkEvalRouter.HandleFunc("/users/{user}", mobileClientsHandler.authorizeMethod(evaluateAllFeatureFlags)).Methods("GET")
	msdkEvalRouter.HandleFunc("/user", mobileClientsHandler.authorizeMethod(evaluateAllFeatureFlags)).Methods("REPORT")

	Info.Printf("Listening on port %d\n", c.Main.Port)

	err = http.ListenAndServe(fmt.Sprintf(":%d", c.Main.Port), router)
	if err != nil {
		if c.Main.ExitOnError {
			Error.Fatalf("Error starting http listener on port: %d  %s", c.Main.Port, err.Error())
		}
		Error.Printf("Error starting http listener on port: %d  %s", c.Main.Port, err.Error())
	}
}

type muxHandler struct {
	clients cmap.ConcurrentMap // A map containing flagReaders or http.Handlers
	baseUri string
}

func (m muxHandler) getStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	envs := make(map[string]StatusEntry)

	healthy := true
	for item := range m.clients.IterBuffered() {
		if item.Val == nil {
			envs[item.Key] = StatusEntry{Status: "disconnected"}
			healthy = false
		} else {
			client := item.Val.(*ld.LDClient)
			if client.Initialized() {
				envs[item.Key] = StatusEntry{Status: "connected"}
			} else {
				envs[item.Key] = StatusEntry{Status: "disconnected"}
				healthy = false
			}
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

func (m muxHandler) authorizeMethod(next func(w http.ResponseWriter, req *http.Request)) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		authKey, err := fetchAuthToken(req)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		client, _ := m.clients.Get(authKey)
		if client == nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("ld-relay is not configured for the provided key"))
			return
		}
		ctx := evalContextImpl{client: client}
		req = req.WithContext(context.WithValue(req.Context(), "context", ctx))
		next(w, req)
	}
}

func (m muxHandler) findEnvironment(next func(w http.ResponseWriter, req *http.Request)) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		envId := mux.Vars(req)["envId"]
		client, _ := m.clients.Get(envId)
		if client == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("ld-relay is not configured for environment id " + envId))
			return
		}
		ctx := evalContextImpl{client: client}
		req = req.WithContext(context.WithValue(req.Context(), "context", ctx))
		next(w, req)
	}
}

func (m muxHandler) getGoals(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	envId, _ := mux.Vars(req)["envId"]

	ldReq, _ := http.NewRequest("GET", m.baseUri+"/sdk/goals/"+envId, nil)
	ldReq.Header.Set("Authorization", req.Header.Get("Authorization"))

	cachingTransport := httpcache.NewMemoryCacheTransport()
	httpClient := cachingTransport.Client()
	res, err := httpClient.Do(ldReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", res.Header["Content-Type"][0])

	defer res.Body.Close()

	w.WriteHeader(res.StatusCode)
	bodyBytes, _ := ioutil.ReadAll(res.Body)
	w.Write(bodyBytes)
}

func serveHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context().Value("context")
	handler := ctx.(EvalContext).getClient().(http.Handler)
	handler.ServeHTTP(w, req)
}

func evaluateAllFeatureFlags(w http.ResponseWriter, req *http.Request) {
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

	ctx := req.Context().Value("context")
	client := ctx.(EvalContext).getClient().(flagReader)
	result, _ := json.Marshal(client.AllFlags(*user))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(result)
	return
}

func ErrorJsonMsg(msg string) (j []byte) {
	j, _ = json.Marshal(errorJson{msg})
	return
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
