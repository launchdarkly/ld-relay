package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/gcfg"
	"github.com/streamrail/concurrent-map"
	ld "gopkg.in/launchdarkly/go-client.v2"
	"gopkg.in/redis.v4"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	Debug   *log.Logger
	Info    *log.Logger
	Warning *log.Logger
	Error   *log.Logger
)

var VERSION = "DEV"

var uuidHeaderPattern = regexp.MustCompile(`^(?:api_key )?((?:[a-z]{3}-)?[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89aAbB][a-f0-9]{3}-[a-f0-9]{12})$`)

type EnvConfig struct {
	ApiKey string
	Prefix string
}

type HostAndPort struct {
	Host string
	Port int
}

func (h HostAndPort) ToAddr() string {
	return fmt.Sprintf("%s:%d", h.Host, h.Port)
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
	Sentinel *struct {
		Master   string
		Nodes    []string `gcfg:"node"`
		DB       int      `gcfg:"db"`
		Password string
	}
	Redis *struct {
		HostAndPort
		DB       int `gcfg:"db"`
		Password string
	}
	Environment map[string]*EnvConfig
}

type StatusEntry struct {
	Status string `json:"status"`
}

var configFile string

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

	publisher := eventsource.NewServer()
	publisher.Gzip = true
	publisher.AllowCORS = true
	publisher.ReplayAll = true

	handlers := cmap.New()

	clients := cmap.New()

	for envName, _ := range c.Environment {
		clients.Set(envName, nil)
	}

	for envName, envConfig := range c.Environment {
		go func(envName string, envConfig EnvConfig) {
			var baseFeatureStore ld.FeatureStore
			if c.Redis != nil && c.Redis.Host != "" && c.Redis.Port != 0 {
				opts := redis.Options{
					Addr: c.Redis.HostAndPort.ToAddr(),
					DB:   c.Redis.DB,
				}

				if c.Redis.Password != "" {
					opts.Password = c.Redis.Password
				}
				baseFeatureStore = ld.NewRedisFeatureStoreWithOptions(&opts, envConfig.Prefix, 0, Info)
			} else if c.Sentinel != nil {
				addrs := make([]string, len(c.Sentinel.Nodes))

				for i, n := range c.Sentinel.Nodes {
					addrs[i] = n
				}

				failoverOpts := redis.FailoverOptions{
					MasterName:    c.Sentinel.Master,
					DB:            c.Sentinel.DB,
					SentinelAddrs: addrs,
				}

				if c.Sentinel.Password != "" {
					failoverOpts.Password = c.Sentinel.Password
				}
				baseFeatureStore = ld.NewRedisFeatureStoreWithFailoverOptions(&failoverOpts, envConfig.Prefix, 0, Info)
			} else {
				baseFeatureStore = ld.NewInMemoryFeatureStore(Info)
			}

			clientConfig := ld.DefaultConfig
			clientConfig.Stream = true
			clientConfig.FeatureStore = NewSSERelayFeatureStore(envConfig.ApiKey, publisher, baseFeatureStore, c.Main.HeartbeatIntervalSecs)
			clientConfig.StreamUri = c.Main.StreamUri
			clientConfig.BaseUri = c.Main.BaseUri

			client, err := ld.MakeCustomClient(envConfig.ApiKey, clientConfig, time.Second*10)

			clients.Set(envName, client)

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
			}
		}(envName, *envConfig)
	}

	http.Handle("/status", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		envs := make(map[string]StatusEntry)

		healthy := true
		for item := range clients.IterBuffered() {
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
	}))

	// Now make a single handler that dispatches to the appropriate handler based on the Authorization header
	http.Handle("/features", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		apiKey, err := fetchAuthToken(req)

		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if h, ok := handlers.Get(apiKey); ok {
			handler := h.(http.Handler)

			handler.ServeHTTP(w, req)
		} else {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))

	Info.Printf("Listening on port %d\n", c.Main.Port)

	http.ListenAndServe(fmt.Sprintf(":%d", c.Main.Port), nil)
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
