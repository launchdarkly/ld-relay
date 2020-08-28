package relay

import (
	"errors"
	"net/http"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/core/logging"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
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
	core    *core.RelayCore
	config  config.Config
	loggers ldlog.Loggers
}

// ClientFactoryFunc is a function that can be used with NewRelay to specify custom behavior when
// Relay needs to create a Go SDK client instance.
type ClientFactoryFunc func(sdkKey config.SDKKey, config ld.Config) (*ld.LDClient, error)

// MakeDefaultLoggers returns a Loggers instance configured with Relay's standard log format.
// Output goes to stdout, except Error level which goes to stderr. Debug level is disabled.
func MakeDefaultLoggers() ldlog.Loggers {
	return logging.MakeDefaultLoggers()
}

// NewRelay creates a new Relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
//
// The clientFactory parameter can be nil and is only needed if you want to customize how Relay
// creates the Go SDK client instance.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory ClientFactoryFunc) (*Relay, error) {
	return newRelayInternal(c, loggers, sdks.ClientFactoryFromLDClientFactory(clientFactory))
}

func newRelayInternal(c config.Config, loggers ldlog.Loggers, clientFactory sdks.ClientFactoryFunc) (*Relay, error) {
	// The "must have at least one environment" check is not included in config.Validate because it will not
	// always be applicable for all Relay variants
	if len(c.Environment) == 0 {
		return nil, errNoEnvironments
	}

	core, err := core.NewRelayCore(
		c,
		loggers,
		clientFactory,
		version.Version,
		"LDRelay/"+version.Version,
		relayenv.LogNameIsSDKKey,
	)
	if err != nil {
		return nil, err
	}

	r := Relay{
		core:    core,
		config:  c,
		loggers: loggers,
	}

	if c.Main.ExitAlways {
		loggers.Info("Running in one-shot mode - will exit immediately after initializing environments")
		// Just wait until all clients have either started or failed, then exit without bothering
		// to set up HTTP handlers.
		err := r.core.WaitForAllClients(0)
		return &r, err
	}

	r.Handler = core.MakeRouter()
	return &r, nil
}

// Close shuts down components created by the Relay.
//
// Currently this includes only the metrics components; it does not close SDK clients.
func (r *Relay) Close() error {
	r.core.Close()
	return nil
}
