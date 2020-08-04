package relay

import (
	"net/http"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
)

const (
	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"
)

// Relay relays endpoints to and from the LaunchDarkly service
type Relay struct {
	http.Handler
	core    *RelayCore
	config  config.Config
	loggers ldlog.Loggers
}

type evalXResult struct {
	Value                ldvalue.Value               `json:"value"`
	Variation            *int                        `json:"variation,omitempty"`
	Version              int                         `json:"version"`
	DebugEventsUntilDate *ldtime.UnixMillisecondTime `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool                        `json:"trackEvents,omitempty"`
	TrackReason          bool                        `json:"trackReason,omitempty"`
	Reason               *ldreason.EvaluationReason  `json:"reason,omitempty"`
}

// NewRelay creates a new relay given a configuration and a method to create a client.
//
// If any metrics exporters are enabled in c.MetricsConfig, it also registers those in OpenCensus.
func NewRelay(c config.Config, loggers ldlog.Loggers, clientFactory sdkconfig.ClientFactoryFunc) (*Relay, error) {
	core, err := NewRelayCore(c, loggers, clientFactory)
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
