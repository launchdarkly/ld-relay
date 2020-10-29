package relay

import (
	"errors"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/filedata"
	"github.com/launchdarkly/ld-relay/v6/internal/util"
	"github.com/launchdarkly/ld-relay/v6/relay/version"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

var (
	errNoEnvironments = errors.New("you must specify at least one environment in your configuration")
)

// Relay relays endpoints to and from the LaunchDarkly service
type Relay struct {
	http.Handler
	core             *core.RelayCore
	autoConfigStream *autoconfig.StreamManager
	archiveManager   filedata.ArchiveManagerInterface
	config           config.Config
	loggers          ldlog.Loggers
}

// ClientFactoryFunc is a function that can be used with NewRelay to specify custom behavior when
// Relay needs to create a Go SDK client instance.
type ClientFactoryFunc func(sdkKey config.SDKKey, config ld.Config) (*ld.LDClient, error)

// Using a struct type for this instead of adding parameters to newRelayInternal helps to minimize
// changes to test code whenever we make more things configurable.
type relayInternalOptions struct {
	loggers               ldlog.Loggers
	clientFactory         sdks.ClientFactoryFunc
	archiveManagerFactory func(string, filedata.UpdateHandler, ldlog.Loggers) (filedata.ArchiveManagerInterface, error)
}

// NewRelay creates a new Relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
//
// The clientFactory parameter can be nil and is only needed if you want to customize how Relay
// creates the Go SDK client instance.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory ClientFactoryFunc) (*Relay, error) {
	return newRelayInternal(c, relayInternalOptions{
		loggers:       loggers,
		clientFactory: sdks.ClientFactoryFromLDClientFactory(clientFactory),
	})
}

func newRelayInternal(c config.Config, options relayInternalOptions) (*Relay, error) {
	var thingsToCleanUp util.CleanupTasks // keeps track of partially constructed things in case we exit early
	defer thingsToCleanUp.Run()

	userAgent := "LDRelay/" + version.Version
	hasAutoConfigKey := c.AutoConfig.Key != ""
	hasFileDataSource := c.OfflineMode.FileDataSource != ""

	if !hasAutoConfigKey && !hasFileDataSource && len(c.Environment) == 0 {
		return nil, errNoEnvironments
	}

	logNameMode := relayenv.LogNameIsSDKKey
	if hasAutoConfigKey {
		logNameMode = relayenv.LogNameIsEnvID
	}

	core, err := core.NewRelayCore(
		c,
		options.loggers,
		options.clientFactory,
		version.Version,
		userAgent,
		logNameMode,
	)
	if err != nil {
		return nil, err
	}
	thingsToCleanUp.AddFunc(core.Close)

	r := &Relay{
		core:    core,
		config:  c,
		loggers: options.loggers,
	}

	if hasAutoConfigKey {
		httpConfig, err := httpconfig.NewHTTPConfig(
			c.Proxy,
			c.AutoConfig.Key,
			userAgent,
			core.Loggers,
		)
		if err != nil {
			return nil, err
		}
		r.autoConfigStream = autoconfig.NewStreamManager(
			c.AutoConfig.Key,
			c.Main.StreamURI.String(),
			relayAutoConfigActions{r},
			httpConfig,
			0,
			core.Loggers,
		)
		_ = r.autoConfigStream.Start()
	}

	if hasFileDataSource {
		factory := options.archiveManagerFactory
		if factory == nil {
			factory = defaultArchiveManagerFactory
		}
		archiveManager, err := factory(
			c.OfflineMode.FileDataSource,
			&relayFileDataActions{r: r},
			core.Loggers,
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
		err := r.core.WaitForAllClients(0)
		if err != nil {
			return nil, err
		}
	}

	r.Handler = core.MakeRouter()
	thingsToCleanUp.Clear() // we succeeded, don't close anything
	return r, nil
}

func defaultArchiveManagerFactory(filePath string, handler filedata.UpdateHandler, loggers ldlog.Loggers) (
	filedata.ArchiveManagerInterface, error) {
	am, err := filedata.NewArchiveManager(filePath, handler, 0, loggers)
	return am, err
}

// Close shuts down components created by the Relay.
//
// Currently this includes only the metrics components; it does not close SDK clients.
func (r *Relay) Close() error {
	if r.autoConfigStream != nil {
		r.autoConfigStream.Close()
	}
	if r.archiveManager != nil {
		_ = r.archiveManager.Close()
	}
	r.core.Close()
	return nil
}
