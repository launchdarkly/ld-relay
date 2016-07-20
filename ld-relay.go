package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/gcfg"
	"github.com/streamrail/concurrent-map"
	ld "gopkg.in/launchdarkly/go-client.v2"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
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
}

type Config struct {
	Main struct {
		ExitOnError           bool
		StreamUri             string
		BaseUri               string
		Port                  int
		HeartbeatIntervalSecs int
	}
	Environment map[string]*EnvConfig
}

var configFile string

func main() {
	var wg sync.WaitGroup

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

	wg.Add(len(c.Environment))
	for envName, envConfig := range c.Environment {
		go func(envName string, envConfig EnvConfig) {
			defer wg.Done()
			clientConfig := ld.DefaultConfig
			clientConfig.Stream = true
			clientConfig.FeatureStore = NewSSERelayFeatureStore(envConfig.ApiKey, publisher, c.Main.HeartbeatIntervalSecs)
			clientConfig.StreamUri = c.Main.StreamUri
			clientConfig.BaseUri = c.Main.BaseUri

			_, err := ld.MakeCustomClient(envConfig.ApiKey, clientConfig, time.Second*10)

			if err != nil {
				Error.Printf("Error initializing LaunchDarkly client for %s: %+v\n", envName, err)

				if c.Main.ExitOnError {
					os.Exit(1)
				}
			} else {
				Info.Printf("Initialized LaunchDarkly client for %s\n", envName)
				// create a handler from the publisher for this environment
				handler := publisher.Handler(envConfig.ApiKey)
				handlers.Set(envConfig.ApiKey, handler)
			}
		}(envName, *envConfig)
	}

	wg.Wait()

	// Now make a single handler that dispatches to the appropriate handler based on the Authorization header
	http.Handle("/flags", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
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
