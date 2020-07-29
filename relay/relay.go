package relay

import (
	"net/http"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"

	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
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

// NewRelay creates a new Relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
//
// The clientFactory parameter can be nil and is only needed if you want to customize how Relay
// creates the Go SDK client instance.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory ClientFactoryFunc) (*Relay, error) {
	return newRelayInternal(c, loggers, core.ClientFactoryFromLDClientFactory(clientFactory))
}

func newRelayInternal(c config.Config, loggers ldlog.Loggers, clientFactory sdks.ClientFactoryFunc) (*Relay, error) {
	core, err := core.NewRelayCore(c, loggers, clientFactory)
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
