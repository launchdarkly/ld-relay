package relay

import (
	"errors"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/projmanager"

	"github.com/gregjones/httpcache"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"
	"github.com/launchdarkly/ld-relay/v8/internal/util"
	"github.com/launchdarkly/ld-relay/v8/relay/version"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ld "github.com/launchdarkly/go-server-sdk/v7"
)

var (
	errNoEnvironments = errors.New("you must specify at least one environment in your configuration")
)

// The Relay Proxy Auto-Config Protocol has two major versions.
// For Relay < v8, that was '1'.
// For Relay >= v8, that is '2'.
// The second version is capable of sending payload filter data, in PUT/PATCH/DELETE messages. Relay < v8
// is not aware of filters and would throw errors/cease to function if it received such messages.
const rpacProtocolVersion = 2

// Relay represents the overall Relay Proxy application.
//
// It can also be referenced externally in order to embed Relay Proxy functionality into a customized
// application; see docs/in-app.md.
//
// This type deliberately exports no methods other than ServeHTTP and Close. Everything else is an
// implementation detail which is subject to change.
type Relay struct {
	http.Handler
	envsByCredential              *EnvironmentLookup
	metricsManager                *metrics.Manager
	clientFactory                 sdks.ClientFactoryFunc
	serverSideStreamProvider      streams.StreamProvider
	serverSideFlagsStreamProvider streams.StreamProvider
	mobileStreamProvider          streams.StreamProvider
	jsClientStreamProvider        streams.StreamProvider
	clientInitCh                  chan relayenv.EnvContext
	fullyConfigured               bool
	clientSideSDKBaseURL          url.URL
	version                       string
	userAgent                     string
	envLogNameMode                relayenv.LogNameMode
	closed                        bool
	lock                          sync.RWMutex
	autoConfigStream              *autoconfig.StreamManager
	archiveManager                filedata.ArchiveManagerInterface
	config                        config.Config
	loggers                       ldlog.Loggers
}

// ClientFactoryFunc is a function that can be used with NewRelay to specify custom behavior when
// Relay needs to create a Go SDK client instance.
type ClientFactoryFunc func(sdkKey config.SDKKey, config ld.Config) (*ld.LDClient, error)

// Using a struct type for this instead of adding parameters to newRelayInternal helps to minimize
// changes to test code whenever we make more things configurable.
type relayInternalOptions struct {
	loggers               ldlog.Loggers
	clientFactory         sdks.ClientFactoryFunc
	archiveManagerFactory func(path string, monitoringInterval time.Duration, environmentUpdates filedata.UpdateHandler, loggers ldlog.Loggers) (filedata.ArchiveManagerInterface, error)
}

// NewRelay creates a new Relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
//
// The clientFactory parameter can be nil and is only needed if you want to customize how Relay
// creates the Go SDK client instance.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory ClientFactoryFunc) (*Relay, error) {
	realClientFactory := sdks.DefaultClientFactory()
	if clientFactory != nil {
		// There's a function signature mismatch here because we didn't originally include the timeout in the
		// ClientFactoryFunc type, so we have to wrap the function in a way that unfortunately doesn't allow
		// the configured timeout to be passed in
		realClientFactory = sdks.ClientFactoryFromLDClientFactory(
			func(sdkKey string, sdkConfig ld.Config, timeout time.Duration) (*ld.LDClient, error) {
				return clientFactory(config.SDKKey(sdkKey), sdkConfig)
			})
	}
	return newRelayInternal(c, relayInternalOptions{
		loggers:       loggers,
		clientFactory: realClientFactory,
	})
}

func newRelayInternal(c config.Config, options relayInternalOptions) (*Relay, error) {
	var thingsToCleanUp util.CleanupTasks // keeps track of partially constructed things in case we exit early
	defer thingsToCleanUp.Run()

	loggers := options.loggers
	clientFactory := options.clientFactory

	if err := config.ValidateConfig(&c, loggers); err != nil { // in case a not-yet-validated Config was passed to NewRelay
		return nil, err
	}

	hasAutoConfigKey := c.AutoConfig.Key.Defined()
	hasFileDataSource := c.OfflineMode.FileDataSource != ""

	if !hasAutoConfigKey && !hasFileDataSource && len(c.Environment) == 0 {
		return nil, errNoEnvironments
	}

	logNameMode := relayenv.LogNameIsSDKKey
	if hasAutoConfigKey || hasFileDataSource {
		logNameMode = relayenv.LogNameIsEnvID
	}

	if clientFactory == nil {
		clientFactory = sdks.DefaultClientFactory()
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

	userAgent := "LDRelay/" + version.Version

	r := &Relay{
		envsByCredential:              NewEnvironmentLookup(),
		serverSideStreamProvider:      streams.NewStreamProvider(basictypes.ServerSideStream, maxConnTime),
		serverSideFlagsStreamProvider: streams.NewStreamProvider(basictypes.ServerSideFlagsOnlyStream, maxConnTime),
		mobileStreamProvider:          streams.NewStreamProvider(basictypes.MobilePingStream, maxConnTime),
		jsClientStreamProvider:        streams.NewStreamProvider(basictypes.JSClientPingStream, maxConnTime),
		metricsManager:                metricsManager,
		clientFactory:                 clientFactory,
		clientInitCh:                  clientInitCh,
		version:                       version.Version,
		userAgent:                     userAgent,
		envLogNameMode:                logNameMode,
		config:                        c,
		loggers:                       loggers,
	}

	thingsToCleanUp.AddCloser(r)

	r.clientSideSDKBaseURL = *c.Main.ClientSideBaseURI.Get() // config.ValidateConfig has ensured that this has a value

	for envName, envConfig := range makeFilteredEnvironments(&c) {
		env, resultCh, err := r.addEnvironment(relayenv.EnvIdentifiers{ConfiguredName: envName}, *envConfig, nil)
		if err != nil {
			return nil, err
		}
		thingsToCleanUp.AddCloser(env)
		go func() {
			env := <-resultCh
			r.clientInitCh <- env
		}()
	}

	if len(c.Environment) > 0 || c.OfflineMode.FileDataSource != "" {
		r.fullyConfigured = true // it's only in auto-config mode that we have any interval of not knowing what the environments are
	}

	if hasAutoConfigKey {
		httpConfig, err := httpconfig.NewHTTPConfig(
			c.Proxy,
			c.AutoConfig.Key,
			userAgent,
			loggers,
		)
		if err != nil {
			return nil, err
		}
		r.autoConfigStream = autoconfig.NewStreamManager(
			c.AutoConfig.Key,
			c.Main.StreamURI.Get(),
			projmanager.NewProjectRouter(&relayAutoConfigActions{r}, loggers),
			httpConfig,
			0,
			rpacProtocolVersion,
			loggers,
		)
		autoConfigResult := r.autoConfigStream.Start()
		go func() {
			err := <-autoConfigResult
			if err != nil {
				// This channel only emits a non-nil error if it's an unrecoverable error, in which case
				// Relay should quit. The ExitOnError option doesn't affect this, because a failure of
				// auto-config is more serious than any environment-specific failure; Relay can't possibly
				// do anything useful without a configuration. The StreamManager has already logged the
				// error by this point, so we just need to quit.
				os.Exit(1)
			}
		}()
	}

	if hasFileDataSource {
		factory := options.archiveManagerFactory
		if factory == nil {
			factory = defaultArchiveManagerFactory
		}
		archiveManager, err := factory(
			c.OfflineMode.FileDataSource,
			c.OfflineMode.FileDataSourceMonitoringInterval.GetOrElse(0),
			&relayFileDataActions{r: r},
			loggers,
		)
		if err != nil {
			return nil, err
		}
		r.archiveManager = archiveManager
		thingsToCleanUp.AddCloser(archiveManager)
	}

	if c.Main.ExitAlways {
		options.loggers.Info("Running in one-shot mode - will exit immediately after initializing environments")
		// Just wait until all clients have either started or failed, then exit without bothering
		// to set up HTTP handlers.
		err := r.waitForAllClients(0)
		if err != nil {
			return nil, err
		}
	}

	r.Handler = r.makeRouter()
	thingsToCleanUp.Clear() // we succeeded, don't close anything
	return r, nil
}

func makeFilteredEnvironments(c *config.Config) map[string]*config.EnvConfig {
	if c.Filters == nil {
		return c.Environment
	}
	out := make(map[string]*config.EnvConfig)
	type namedEnv struct {
		name   string
		config *config.EnvConfig
	}
	byProj := make(map[string][]*namedEnv)

	for k, v := range c.Environment {
		byProj[v.ProjKey] = append(byProj[v.ProjKey], &namedEnv{name: k, config: v})
	}

	for projKey, envs := range byProj {
		// First, add the default environments for a project
		for _, e := range envs {
			out[e.name] = e.config
		}
		associatedFilters, ok := c.Filters[projKey]
		if ok {
			for _, filterKey := range associatedFilters.Keys.Values() {
				key := strings.Trim(filterKey, " ")
				for _, e := range envs {
					copied := *e.config
					copied.FilterKey = config.FilterKey(key)
					if copied.Prefix != "" {
						copied.Prefix = copied.Prefix + "/" + key
					}
					out[e.name+"/"+key] = &copied
				}
			}
		}
	}

	return out
}

func defaultArchiveManagerFactory(filePath string, monitoringInterval time.Duration, handler filedata.UpdateHandler, loggers ldlog.Loggers) (
	filedata.ArchiveManagerInterface, error) {
	am, err := filedata.NewArchiveManager(filePath, handler, monitoringInterval, loggers)
	return am, err
}

// Close shuts down components created by the Relay Proxy.
//
// This includes dropping all connections to the LaunchDarkly services and to SDK clients,
// closing database connections if any, and stopping all Relay port listeners, goroutines,
// and OpenCensus exporters.
func (r *Relay) Close() error {
	r.lock.Lock()
	if r.closed {
		r.lock.Unlock()
		return nil
	}

	r.closed = true
	r.lock.Unlock()

	r.metricsManager.Close()

	if r.autoConfigStream != nil {
		r.autoConfigStream.Close()
	}
	if r.archiveManager != nil {
		_ = r.archiveManager.Close()
	}

	for _, env := range r.envsByCredential.Environments() {
		if err := env.Close(); err != nil {
			r.loggers.Warnf("unexpected error when closing environment: %s", err)
		}
	}

	for _, sp := range r.allStreamProviders() {
		sp.Close()
	}

	return nil
}

func (r *Relay) allStreamProviders() []streams.StreamProvider {
	return []streams.StreamProvider{
		r.serverSideStreamProvider,
		r.serverSideFlagsStreamProvider,
		r.mobileStreamProvider,
		r.jsClientStreamProvider,
	}
}

var errRelayNotReady = errors.New("relay is not yet fully configured")
var errUnrecognizedEnvironment = errors.New("no environment corresponds to given credentials")
var errPayloadFilterNotFound = errors.New("credential corresponds to an environment but filter is unrecognized")

func IsNotReady(err error) bool {
	return err == errRelayNotReady
}

func IsUnrecognizedEnvironment(err error) bool {
	return err == errUnrecognizedEnvironment
}

func IsPayloadFilterNotFound(err error) bool {
	return err == errPayloadFilterNotFound
}

// getEnvironment returns the environment object corresponding to the given credential, or nil
// if not found. The credential can be an SDK key, a mobile key, or an environment ID. The second
// return value is normally true, but is false if Relay does not yet have a valid configuration
// (which affects our error handling).
func (r *Relay) getEnvironment(req sdkauth.ScopedCredential) (relayenv.EnvContext, error) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	if r.fullyConfigured {
		env, found := r.envsByCredential.Lookup(req)
		if found {
			return env, nil
		}
		// This secondary lookup is necessary to present a 404 to downstream SDKs if the credential was correct
		// but the filter wrong, to mirror LaunchDarkly behavior.
		if _, foundUnfiltered := r.envsByCredential.Lookup(req.Unscope()); foundUnfiltered {
			return nil, errPayloadFilterNotFound
		}
		return nil, errUnrecognizedEnvironment
	}

	return nil, errRelayNotReady
}

// getAllEnvironments returns all currently configured environments.
func (r *Relay) getAllEnvironments() []relayenv.EnvContext {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.envsByCredential.Environments()
}

// addEnvironment attempts to add a new environment. It returns an error only if the configuration
// is invalid; it does not wait to see whether the connection to LaunchDarkly succeeded.
func (r *Relay) addEnvironment(
	identifiers relayenv.EnvIdentifiers,
	envConfig config.EnvConfig,
	transformClientConfig func(ld.Config) ld.Config,
) (relayenv.EnvContext, <-chan relayenv.EnvContext, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if r.closed {
		return nil, nil, errAlreadyClosed
	}

	dataStoreFactory, dataStoreInfo, err := sdks.ConfigureDataStore(r.config, envConfig, r.loggers)
	if err != nil {
		return nil, nil, err
	}

	resultCh := make(chan relayenv.EnvContext, 1)

	var jsClientContext relayenv.JSClientContext

	if envConfig.EnvID.Defined() {
		jsClientContext.Origins = envConfig.AllowedOrigin.Values()
		jsClientContext.Headers = envConfig.AllowedHeader.Values()

		cachingTransport := httpcache.NewMemoryCacheTransport()
		jsClientContext.Proxy = &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				url := req.URL
				url.Scheme = r.clientSideSDKBaseURL.Scheme
				url.Host = r.clientSideSDKBaseURL.Host
				req.Host = r.clientSideSDKBaseURL.Hostname()
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

	wrappedClientFactory := func(sdkKey config.SDKKey, config ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if transformClientConfig != nil {
			config = transformClientConfig(config)
		}
		return r.clientFactory(sdkKey, config, timeout)
	}
	clientContext, err := relayenv.NewEnvContext(relayenv.EnvContextImplParams{
		Identifiers:      identifiers,
		EnvConfig:        envConfig,
		AllConfig:        r.config,
		ClientFactory:    wrappedClientFactory,
		DataStoreFactory: dataStoreFactory,
		DataStoreInfo:    dataStoreInfo,
		StreamProviders:  r.allStreamProviders(),
		JSClientContext:  jsClientContext,
		MetricsManager:   r.metricsManager,
		UserAgent:        r.userAgent,
		LogNameMode:      r.envLogNameMode,
		Loggers:          r.loggers,
	}, resultCh)
	if err != nil {
		return nil, nil, errNewClientContextFailed(identifiers.GetDisplayName(), err)
	}

	r.envsByCredential.InsertEnvironment(clientContext)

	return clientContext, resultCh, nil
}

// removeEnvironment shuts down and removes an existing environment. All network connections, metrics
// resources, and (if applicable) database connections, are immediately closed for this environment.
// Subsequent requests using credentials for this environment will be rejected.
func (r *Relay) removeEnvironment(params sdkauth.ScopedCredential) bool {
	env, found := r.envsByCredential.DeleteEnvironment(params)

	if !found {
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

// setFullyConfigured updates the state of whether Relay has a valid set of environments.
func (r *Relay) setFullyConfigured(fullyConfigured bool) {
	r.lock.Lock()
	r.fullyConfigured = fullyConfigured
	r.lock.Unlock()
}

// addConnectionMapping updates the RelayCore's environment mapping to reflect that a new
// credential is now enabled for this EnvContext. This should be done only *after* calling
// EnvContext.AddCredential() so that if the RelayCore receives an incoming request with the new
// credential immediately after this, it will work.
func (r *Relay) addConnectionMapping(params sdkauth.ScopedCredential, env relayenv.EnvContext) {
	r.envsByCredential.MapRequestParams(params, env)
}

// removeConnectionMapping updates the RelayCore's environment mapping to reflect that this
// credential is no longer enabled. This should be done *before* calling EnvContext.RemoveCredential()
// because RemoveCredential() disconnects all existing streams, and if a client immediately tries to
// reconnect using the same credential we want it to be rejected.
func (r *Relay) removeConnectionMapping(params sdkauth.ScopedCredential) {
	r.envsByCredential.UnmapRequestParams(params)
}

func (r *Relay) setCredentials(existingEnv relayenv.EnvContext, updatedEnv envfactory.EnvironmentParams) {
	// These are an SDK key, a mobile key, and potentially an expiring SDK key. The expiring SDK key is really an indication
	// that if *such a key already exists*, it should be regarded as deprecated (don't accept new connections.)
	updatedCredentials := updatedEnv.Credentials()

	// These are the existing valid credentials that an SDK can use to connect to this Relay environment.
	currentValidCredentials := existingEnv.GetCredentials()

	for _, existingCredential := range currentValidCredentials {
		supersedingCredential, status := existingCredential.Compare(updatedCredentials)
		if status == credential.Unchanged {
			// There's no action to take if a credential remains the same.
			continue
		}

		// Otherwise, the new valid credential needs to be loaded into the environment.
		existingEnv.AddCredential(supersedingCredential)
		r.addConnectionMapping(sdkauth.NewScoped(updatedEnv.Identifiers.FilterKey, supersedingCredential), existingEnv)

		switch status {
		case credential.Deprecated:
			existingEnv.DeprecateCredential(existingCredential)
		case credential.Expired:
			r.removeConnectionMapping(sdkauth.NewScoped(updatedEnv.Identifiers.FilterKey, existingCredential))
			existingEnv.RemoveCredential(existingCredential)
		}
	}
}

// waitForAllClients blocks until all environments that were in the initial configuration have
// reported back as either successfully connected or failed, or until the specified timeout (if the
// timeout is non-zero).
func (r *Relay) waitForAllClients(timeout time.Duration) error {
	numEnvironments := len(r.envsByCredential.Environments())
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
				os.Exit(1) //nolint:gocritic // yes, we know "defer timer.Stop()" won't execute if we exit the process
			}
			return errSomeEnvironmentFailed
		}
		return nil
	case <-timeoutCh:
		return errInitializationTimeout
	}
}
