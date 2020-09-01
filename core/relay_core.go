package core

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	config "github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/core/internal/util"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/streams"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/gregjones/httpcache"
)

var (
	errAlreadyClosed         = errors.New("this Relay was already shut down")
	errDefaultBaseURLInvalid = errors.New("unexpected error: default base URL is invalid")
	errInitializationTimeout = errors.New("timed out waiting for environments to initialize")
	errSomeEnvironmentFailed = errors.New("one or more environments failed to initialize")
)

func errNewClientContextFailed(envName string, err error) error {
	return fmt.Errorf(`unable to create client context for "%s": %w`, envName, err)
}

func errNewMetricsManagerFailed(err error) error {
	return fmt.Errorf("unable to create metrics manager: %w", err)
}

// RelayCore encapsulates the core logic for all variants of Relay Proxy.
type RelayCore struct {
	allEnvironments               []relayenv.EnvContext
	envsByCredential              map[config.SDKCredential]relayenv.EnvContext
	metricsManager                *metrics.Manager
	clientFactory                 sdks.ClientFactoryFunc
	serverSideStreamProvider      streams.StreamProvider
	serverSideFlagsStreamProvider streams.StreamProvider
	mobileStreamProvider          streams.StreamProvider
	jsClientStreamProvider        streams.StreamProvider
	clientInitCh                  chan relayenv.EnvContext
	config                        config.Config
	baseURL                       url.URL
	Version                       string
	userAgent                     string
	envLogNameMode                relayenv.LogNameMode
	Loggers                       ldlog.Loggers
	closed                        bool
	lock                          sync.RWMutex
}

// NewRelayCore creates and configures an instance of RelayCore, and immediately starts initializing
// all configured environments.
func NewRelayCore(
	c config.Config,
	loggers ldlog.Loggers,
	clientFactory sdks.ClientFactoryFunc,
	version string,
	userAgent string,
	envLogNameMode relayenv.LogNameMode,
) (*RelayCore, error) {
	var thingsToCleanUp util.CleanupTasks // keeps track of partially constructed things in case we exit early
	defer thingsToCleanUp.Run()

	if err := config.ValidateConfig(&c, loggers); err != nil { // in case a not-yet-validated Config was passed to NewRelay
		return nil, err
	}

	if clientFactory == nil {
		clientFactory = sdks.DefaultClientFactory
	}

	if c.Main.LogLevel.IsDefined() {
		loggers.SetMinLevel(c.Main.LogLevel.GetOrElse(ldlog.Info))
	}

	metricsManager, err := metrics.NewManager(c.MetricsConfig, 0, loggers)
	if err != nil {
		return nil, errNewMetricsManagerFailed(err)
	}
	thingsToCleanUp.AddFunc(metricsManager.Close)

	clientInitCh := make(chan relayenv.EnvContext, len(c.Environment))

	maxConnTime := c.Main.MaxClientConnectionTime.GetOrElse(0)

	r := RelayCore{
		envsByCredential:              make(map[config.SDKCredential]relayenv.EnvContext),
		serverSideStreamProvider:      streams.NewServerSideStreamProvider(maxConnTime),
		serverSideFlagsStreamProvider: streams.NewServerSideFlagsOnlyStreamProvider(maxConnTime),
		mobileStreamProvider:          streams.NewMobilePingStreamProvider(maxConnTime),
		jsClientStreamProvider:        streams.NewJSClientPingStreamProvider(maxConnTime),
		metricsManager:                metricsManager,
		clientFactory:                 clientFactory,
		clientInitCh:                  clientInitCh,
		config:                        c,
		Version:                       version,
		userAgent:                     userAgent,
		envLogNameMode:                envLogNameMode,
		Loggers:                       loggers,
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
		env, resultCh, err := r.AddEnvironment(relayenv.EnvIdentifiers{ConfiguredName: envName}, *envConfig)
		if err != nil {
			return nil, err
		}
		thingsToCleanUp.AddCloser(env)
		go func() {
			env := <-resultCh
			r.clientInitCh <- env
		}()
	}

	thingsToCleanUp.Clear() // we've succeeded so we do not want to throw away these things

	return &r, nil
}

// GetEnvironment returns the environment object corresponding to the given credential, or nil
// if not found. The credential can be an SDK key, a mobile key, or an environment ID.
func (r *RelayCore) GetEnvironment(credential config.SDKCredential) relayenv.EnvContext {
	r.lock.RLock()
	defer r.lock.RUnlock()

	return r.envsByCredential[credential]
}

// GetAllEnvironments returns all currently configured environments.
func (r *RelayCore) GetAllEnvironments() []relayenv.EnvContext {
	r.lock.RLock()
	defer r.lock.RUnlock()

	ret := make([]relayenv.EnvContext, len(r.allEnvironments))
	copy(ret, r.allEnvironments)
	return ret
}

// AddEnvironment attempts to add a new environment. It returns an error only if the configuration
// is invalid; it does not wait to see whether the connection to LaunchDarkly succeeded.
func (r *RelayCore) AddEnvironment(
	identifiers relayenv.EnvIdentifiers,
	envConfig config.EnvConfig,
) (relayenv.EnvContext, <-chan relayenv.EnvContext, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.closed {
		return nil, nil, errAlreadyClosed
	}

	dataStoreFactory, err := sdks.ConfigureDataStore(r.config, envConfig, r.Loggers)
	if err != nil {
		return nil, nil, err
	}

	resultCh := make(chan relayenv.EnvContext, 1)

	var jsClientContext relayenv.JSClientContext

	if envConfig.EnvID != "" {
		jsClientContext.Origins = envConfig.AllowedOrigin.Values()

		cachingTransport := httpcache.NewMemoryCacheTransport()
		jsClientContext.Proxy = &httputil.ReverseProxy{
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
	}

	clientContext, err := relayenv.NewEnvContext(
		identifiers,
		envConfig,
		r.config,
		r.clientFactory,
		dataStoreFactory,
		r.allStreamProviders(),
		jsClientContext,
		r.metricsManager,
		r.userAgent,
		r.envLogNameMode,
		r.Loggers,
		resultCh,
	)
	if err != nil {
		return nil, nil, errNewClientContextFailed(identifiers.GetDisplayName(), err)
	}

	r.allEnvironments = append(r.allEnvironments, clientContext)
	r.envsByCredential[envConfig.SDKKey] = clientContext
	if envConfig.MobileKey != "" {
		r.envsByCredential[envConfig.MobileKey] = clientContext
	}
	if envConfig.EnvID != "" {
		r.envsByCredential[envConfig.EnvID] = clientContext
	}

	return clientContext, resultCh, nil
}

// RemoveEnvironment shuts down and removes an existing environment. All network connections, metrics
// resources, and (if applicable) database connections, are immediately closed for this environment.
// Subsequent requests using credentials for this environment will be rejected.
//
// It returns true if successful, or false if there was no such environment.
func (r *RelayCore) RemoveEnvironment(env relayenv.EnvContext) bool {
	r.lock.Lock()

	found := false
	for i, e := range r.allEnvironments {
		if e == env {
			r.allEnvironments = append(r.allEnvironments[:i], r.allEnvironments[i+1:]...)
			found = true
			break
		}
	}

	if found {
		for _, c := range env.GetCredentials() {
			delete(r.envsByCredential, c)
		}
	}

	r.lock.Unlock()

	if !found {
		return false
	}

	// At this point any more incoming requests that try to use this environment's credentials will
	// be rejected, since it's already been removed from all of our maps above. Now, calling Close()
	// on the environment will do the rest of the cleanup and disconnect any current clients.
	if err := env.Close(); err != nil {
		r.Loggers.Warnf("unexpected error when closing environment: %s", err)
	}

	return true
}

// AddedEnvironmentCredential updates the RelayCore's environment mapping to reflect that a new
// credential is now enabled for this EnvContext. This should be done only *after* calling
// EnvContext.AddCredential() so that if the RelayCore receives an incoming request with the new
// credential immediately after this, it will work.
func (r *RelayCore) AddedEnvironmentCredential(env relayenv.EnvContext, newCredential config.SDKCredential) {
	r.lock.Lock()
	r.envsByCredential[newCredential] = env
	r.lock.Unlock()
}

// RemovingEnvironmentCredential updates the RelayCore's environment mapping to reflect that this
// credential is no longer enabled. This should be done *before* calling EnvContext.RemoveCredential()
// because RemoveCredential() disconnects all existing streams, and if a client immediately tries to
// reconnect using the same credential we want it to be rejected.
func (r *RelayCore) RemovingEnvironmentCredential(oldCredential config.SDKCredential) {
	r.lock.Lock()
	delete(r.envsByCredential, oldCredential)
	r.lock.Unlock()
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
			if r.config.Main.ExitOnError {
				break // ExitOnError implies we shouldn't wait for more than one error
			}
		}
		resultCh <- failed
	}()

	select {
	case failed := <-resultCh:
		if failed {
			if r.config.Main.ExitOnError {
				os.Exit(1)
			}
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
	r.envsByCredential = nil

	r.lock.Unlock()

	r.metricsManager.Close()
	for _, env := range envs {
		if err := env.Close(); err != nil {
			r.Loggers.Warnf("unexpected error when closing environment: %s", err)
		}
	}

	for _, sp := range r.allStreamProviders() {
		sp.Close()
	}
}

func (r *RelayCore) allStreamProviders() []streams.StreamProvider {
	return []streams.StreamProvider{
		r.serverSideStreamProvider,
		r.serverSideFlagsStreamProvider,
		r.mobileStreamProvider,
		r.jsClientStreamProvider,
	}
}
