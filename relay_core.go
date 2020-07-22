package relay

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gregjones/httpcache"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type RelayEnvironments interface { //nolint:golint // yes, we know the package name is also "relay"
	GetEnvironment(config.SDKCredential) relayenv.EnvContext
	GetAllEnvironments() map[config.SDKKey]relayenv.EnvContext
}

type RelayCore struct { //nolint:golint // yes, we know the package name is also "relay"
	allEnvironments map[config.SDKKey]relayenv.EnvContext
	envsByMobileKey map[config.MobileKey]relayenv.EnvContext
	envsByEnvID     map[config.EnvironmentID]*clientSideContext
	metricsManager  *metrics.Manager
	clientFactory   sdkconfig.ClientFactoryFunc
	allPublisher    *eventsource.Server
	flagsPublisher  *eventsource.Server
	pingPublisher   *eventsource.Server
	clientInitCh    chan relayenv.EnvContext
	config          config.Config
	baseURL         url.URL
	loggers         ldlog.Loggers
	lock            sync.RWMutex
}

func NewRelayCore(
	c config.Config,
	loggers ldlog.Loggers,
	clientFactory sdkconfig.ClientFactoryFunc,
) (*RelayCore, error) {
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

	clientInitCh := make(chan relayenv.EnvContext, len(c.Environment))

	r := RelayCore{
		allEnvironments: make(map[config.SDKKey]relayenv.EnvContext),
		envsByMobileKey: make(map[config.MobileKey]relayenv.EnvContext),
		envsByEnvID:     make(map[config.EnvironmentID]*clientSideContext),
		metricsManager:  metricsManager,
		clientFactory:   clientFactory,
		clientInitCh:    clientInitCh,
		config:          c,
		loggers:         loggers,
	}

	makeSSEServer := func() *eventsource.Server {
		s := eventsource.NewServer()
		s.Gzip = false
		s.AllowCORS = true
		s.ReplayAll = true
		s.MaxConnTime = c.Main.MaxClientConnectionTime.GetOrElse(0)
		return s
	}
	r.allPublisher = makeSSEServer()
	r.flagsPublisher = makeSSEServer()
	r.pingPublisher = makeSSEServer()

	if len(c.Environment) == 0 {
		return nil, fmt.Errorf("you must specify at least one environment in your configuration")
	}

	if c.Main.BaseURI.IsDefined() {
		r.baseURL = *c.Main.BaseURI.Get()
	} else {
		u, err := url.Parse(config.DefaultBaseURI)
		if err != nil {
			return nil, errors.New("unexpected error: default base URI is invalid")
		}
		r.baseURL = *u
	}

	for envName, envConfig := range c.Environment {
		if envConfig == nil {
			loggers.Warnf("environment config was nil for environment %q; ignoring", envName)
			continue
		}
		err := r.AddEnvironment(envName, *envConfig)
		if err != nil {
			for _, env := range r.allEnvironments {
				_ = env.Close()
			}
			return nil, err
		}
	}

	return &r, nil
}

func (r *RelayCore) AddEnvironment(envName string, envConfig config.EnvConfig) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	dataStoreFactory, err := sdkconfig.ConfigureDataStore(r.config, envConfig, r.loggers)
	if err != nil {
		return err
	}

	clientContext, err := relayenv.NewEnvContext(
		envName,
		envConfig,
		r.config,
		r.clientFactory,
		dataStoreFactory,
		r.allPublisher,
		r.flagsPublisher,
		r.pingPublisher,
		r.metricsManager,
		r.loggers,
		r.clientInitCh,
	)
	if err != nil {
		return fmt.Errorf(`unable to create client context for "%s": %s`, envName, err)
	}
	r.allEnvironments[envConfig.SDKKey] = clientContext
	if envConfig.MobileKey != "" {
		r.envsByMobileKey[envConfig.MobileKey] = clientContext
	}

	if envConfig.EnvID != "" {
		allowedOrigins := envConfig.AllowedOrigin.Values()
		cachingTransport := httpcache.NewMemoryCacheTransport()
		if envConfig.InsecureSkipVerify {
			tlsConfig := &tls.Config{InsecureSkipVerify: envConfig.InsecureSkipVerify} // nolint:gas // allow this because the user has to explicitly enable it
			defaultTransport := http.DefaultTransport.(*http.Transport)
			transport := &http.Transport{ // we can't just copy defaultTransport all at once because it has a Mutex
				Proxy:                 defaultTransport.Proxy,
				DialContext:           defaultTransport.DialContext,
				ForceAttemptHTTP2:     defaultTransport.ForceAttemptHTTP2,
				MaxIdleConns:          defaultTransport.MaxIdleConns,
				IdleConnTimeout:       defaultTransport.IdleConnTimeout,
				TLSClientConfig:       tlsConfig,
				TLSHandshakeTimeout:   defaultTransport.TLSHandshakeTimeout,
				ExpectContinueTimeout: defaultTransport.ExpectContinueTimeout,
			}
			cachingTransport.Transport = transport
		}

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				url := req.URL
				url.Scheme = r.baseURL.Scheme
				url.Host = r.baseURL.Host
				req.Host = r.baseURL.Hostname()
			},
			ModifyResponse: func(resp *http.Response) error {
				// Leave access control to our own cors middleware
				for h := range resp.Header {
					if strings.HasPrefix(strings.ToLower(h), "access-control") {
						resp.Header.Del(h)
					}
				}
				return nil
			},
			Transport: cachingTransport,
		}

		r.envsByEnvID[envConfig.EnvID] = &clientSideContext{
			EnvContext:     clientContext,
			proxy:          proxy,
			allowedOrigins: allowedOrigins,
		}
	}

	return nil
}

func (r *RelayCore) GetEnvironment(credential config.SDKCredential) relayenv.EnvContext {
	r.lock.RLock()
	defer r.lock.RUnlock()

	switch c := credential.(type) {
	case config.SDKKey:
		return r.allEnvironments[c]
	case config.MobileKey:
		return r.envsByMobileKey[c]
	case config.EnvironmentID:
		return r.envsByEnvID[c]
	default:
		return nil
	}
}

func (r *RelayCore) GetAllEnvironments() map[config.SDKKey]relayenv.EnvContext {
	r.lock.RLock()
	defer r.lock.RUnlock()

	ret := make(map[config.SDKKey]relayenv.EnvContext, len(r.allEnvironments))
	for k, v := range r.allEnvironments {
		ret[k] = v
	}
	return ret
}

func (r *RelayCore) WaitForAllClients(timeout time.Duration) error {
	numEnvironments := len(r.allEnvironments)
	numFinished := 0

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	resultCh := make(chan bool, 1)
	go func() {
		failed := false
		for numFinished < numEnvironments {
			ctx := <-r.clientInitCh
			numFinished++
			if ctx.GetInitError() != nil {
				failed = true
			}
		}
		resultCh <- failed
	}()

	select {
	case failed := <-resultCh:
		if failed {
			return errors.New("one or more environments failed to initialize")
		}
		return nil
	case <-timeoutCh:
		return errors.New("timed out waiting for environments to initialize")
	}
}

func (r *RelayCore) Close() {
	r.metricsManager.Close()
	for _, env := range r.allEnvironments {
		_ = env.Close()
	}
}
