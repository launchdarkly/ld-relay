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

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/streams"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var (
	errAlreadyClosed         = errors.New("this Relay was already shut down")
	errDefaultBaseURLInvalid = errors.New("unexpected error: default base URL is invalid")
	errInitializationTimeout = errors.New("timed out waiting for environments to initialize")
	errNoEnvironments        = errors.New("you must specify at least one environment in your configuration")
	errSomeEnvironmentFailed = errors.New("one or more environments failed to initialize")
)

func errNewClientContextFailed(envName string, err error) error {
	return fmt.Errorf(`unable to create client context for "%s": %w`, envName, err)
}

func errNewMetricsManagerFailed(err error) error {
	return fmt.Errorf("unable to create metrics manager: %w", err)
}

// RelayEnvironments defines the methods for looking up environments. This is represented as an interface
// so that test code can mock that capability.
type RelayEnvironments interface { //nolint:golint // yes, we know the package name is also "relay"
	GetEnvironment(config.SDKCredential) relayenv.EnvContext
	GetAllEnvironments() map[config.SDKKey]relayenv.EnvContext
}

// RelayCore encapsulates the core logic for all variants of Relay Proxy.
type RelayCore struct { //nolint:golint // yes, we know the package name is also "relay"
	allEnvironments map[config.SDKKey]relayenv.EnvContext
	envsByMobileKey map[config.MobileKey]relayenv.EnvContext
	envsByEnvID     map[config.EnvironmentID]*clientSideContext
	metricsManager  *metrics.Manager
	clientFactory   sdkconfig.ClientFactoryFunc
	publishers      *streams.Publishers
	clientInitCh    chan relayenv.EnvContext
	config          config.Config
	baseURL         url.URL
	loggers         ldlog.Loggers
	closed          bool
	lock            sync.RWMutex
}

// NewRelayCore creates and configures an instance of RelayCore, and immediately starts initializing
// all configured environments.
func NewRelayCore(
	c config.Config,
	loggers ldlog.Loggers,
	clientFactory sdkconfig.ClientFactoryFunc,
) (*RelayCore, error) {
	if err := config.ValidateConfig(&c, loggers); err != nil { // in case a not-yet-validated Config was passed to NewRelay
		return nil, err
	}

	if len(c.Environment) == 0 {
		return nil, errNoEnvironments
	}

	if c.Main.LogLevel.IsDefined() {
		loggers.SetMinLevel(c.Main.LogLevel.GetOrElse(ldlog.Info))
	}

	metricsManager, err := metrics.NewManager(c.MetricsConfig, 0, loggers)
	if err != nil {
		return nil, errNewMetricsManagerFailed(err)
	}

	clientInitCh := make(chan relayenv.EnvContext, len(c.Environment))

	r := RelayCore{
		allEnvironments: make(map[config.SDKKey]relayenv.EnvContext),
		envsByMobileKey: make(map[config.MobileKey]relayenv.EnvContext),
		envsByEnvID:     make(map[config.EnvironmentID]*clientSideContext),
		publishers:      streams.NewPublishers(c.Main.MaxClientConnectionTime.GetOrElse(0)),
		metricsManager:  metricsManager,
		clientFactory:   clientFactory,
		clientInitCh:    clientInitCh,
		config:          c,
		loggers:         loggers,
	}

	if c.Main.BaseURI.IsDefined() {
		r.baseURL = *c.Main.BaseURI.Get()
	} else {
		u, err := url.Parse(config.DefaultBaseURI)
		if err != nil {
			return nil, errDefaultBaseURLInvalid
		}
		r.baseURL = *u
	}

	for envName, envConfig := range c.Environment {
		if envConfig == nil {
			loggers.Warnf("environment config was nil for environment %q; ignoring", envName)
			continue
		}
		resultCh, err := r.AddEnvironment(envName, *envConfig)
		if err != nil {
			for _, env := range r.allEnvironments {
				_ = env.Close()
			}
			return nil, err
		}
		go func() {
			env := <-resultCh
			r.clientInitCh <- env
		}()
	}

	return &r, nil
}

// GetEnvironment returns the environment object corresponding to the given credential, or nil
// if not found. The credential can be an SDK key, a mobile key, or an environment ID.
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

// GetAllEnvironments returns all currently configured environments, indexed by SDK key.
func (r *RelayCore) GetAllEnvironments() map[config.SDKKey]relayenv.EnvContext {
	r.lock.RLock()
	defer r.lock.RUnlock()

	ret := make(map[config.SDKKey]relayenv.EnvContext, len(r.allEnvironments))
	for k, v := range r.allEnvironments {
		ret[k] = v
	}
	return ret
}

// AddEnvironment attempts to add a new environment. It returns an error only if the configuration
// is invalid; it does not wait to see whether the connection to LaunchDarkly succeeded.
func (r *RelayCore) AddEnvironment(envName string, envConfig config.EnvConfig) (<-chan relayenv.EnvContext, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.closed {
		return nil, errAlreadyClosed
	}

	dataStoreFactory, err := sdkconfig.ConfigureDataStore(r.config, envConfig, r.loggers)
	if err != nil {
		return nil, err
	}

	resultCh := make(chan relayenv.EnvContext, 1)

	clientContext, err := relayenv.NewEnvContext(
		envName,
		envConfig,
		r.config,
		r.clientFactory,
		dataStoreFactory,
		r.publishers,
		r.metricsManager,
		r.loggers,
		resultCh,
	)
	if err != nil {
		return nil, errNewClientContextFailed(envName, err)
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

	return resultCh, nil
}

// RemoveEnvironment shuts down and removes an existing environment. All network connections, metrics
// resources, and (if applicable) database connections, are immediately closed for this environment.
// Subsequent requests using credentials for this environment will be rejected.
//
// It returns true if successful, or false if there was no such environment.
func (r *RelayCore) RemoveEnvironment(sdkKey config.SDKKey) bool {
	r.lock.Lock()
	env := r.allEnvironments[sdkKey]
	if env != nil {
		delete(r.allEnvironments, sdkKey)
		delete(r.envsByMobileKey, env.GetCredentials().MobileKey)
		delete(r.envsByEnvID, env.GetCredentials().EnvironmentID)
	}
	r.lock.Unlock()

	if env == nil {
		return false
	}

	// At this point any more incoming requests that try to use this environment's credentials will
	// be rejected, since it's already been removed from all of our maps above. Now, calling Close()
	// on the environment will do the rest of the cleanup and disconnect any current clients.
	if err := env.Close(); err != nil {
		r.loggers.Warnf("unexpected error when closing environment: %s", err)
	}

	return true
}

// WaitForAllClients blocks until all environments that were in the initial configuration have
// reported back as either successfully connected or failed, or until the specified timeout (if the
// timeout is non-zero).
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
			return errSomeEnvironmentFailed
		}
		return nil
	case <-timeoutCh:
		return errInitializationTimeout
	}
}

// Close shuts down all existing environments and releases all resources used by RelayCore.
func (r *RelayCore) Close() {
	r.lock.Lock()
	if r.closed {
		r.lock.Unlock()
		return
	}

	r.closed = true

	envs := r.allEnvironments
	r.allEnvironments = nil
	r.envsByMobileKey = nil
	r.envsByEnvID = nil

	r.lock.Unlock()

	r.metricsManager.Close()
	for _, env := range envs {
		if err := env.Close(); err != nil {
			r.loggers.Warnf("unexpected error when closing environment: %s", err)
		}
	}
	r.publishers.Close()
}
