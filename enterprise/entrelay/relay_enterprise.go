package entrelay

import (
	"errors"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/enterprise/autoconfig"
	"github.com/launchdarkly/ld-relay/v6/enterprise/version"
	"github.com/launchdarkly/ld-relay/v6/internal/core"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var (
	errNoEnvironments = errors.New("you must specify at least one environment in your configuration if you are not using auto-configuration")
)

// RelayEnterprise is the main object for Relay Proxy Enterprise. Most of its functionality comes from RelayCore.
type RelayEnterprise struct {
	core             *core.RelayCore
	config           config.Config
	handler          http.Handler
	autoConfigStream *autoconfig.StreamManager
}

// NewRelayEnterprise creates a new RelayEnterprise instance.
func NewRelayEnterprise(
	c config.Config,
	loggers ldlog.Loggers,
	clientFactory sdks.ClientFactoryFunc,
) (*RelayEnterprise, error) {
	userAgent := "LDRelayEnterprise/" + version.Version

	hasAutoConfigKey := c.AutoConfig.Key != ""

	if !hasAutoConfigKey && len(c.Environment) == 0 {
		return nil, errNoEnvironments
	}

	logNameMode := relayenv.LogNameIsSDKKey
	if hasAutoConfigKey {
		logNameMode = relayenv.LogNameIsEnvID
	}

	core, err := core.NewRelayCore(
		c,
		loggers,
		clientFactory,
		version.Version,
		userAgent,
		logNameMode,
	)
	if err != nil {
		return nil, err
	}

	r := &RelayEnterprise{
		core:   core,
		config: c,
	}

	if hasAutoConfigKey {
		httpConfig, err := httpconfig.NewHTTPConfig(
			c.Proxy,
			c.AutoConfig.Key,
			userAgent,
			core.Loggers,
		)
		if err != nil {
			core.Close()
			return nil, err
		}
		r.autoConfigStream = autoconfig.NewStreamManager(
			c.AutoConfig.Key,
			c.Main.StreamURI.String(),
			r, // r implements autoconfig.MessageHandler - see relay_enterprise_autoconfig.go
			httpConfig,
			0,
			core.Loggers,
		)
		_ = r.autoConfigStream.Start()
	}

	r.handler = r.core.MakeRouter()

	return r, nil
}

// GetHandler returns the main HTTP handler for the Relay Enterprise instance.
func (r *RelayEnterprise) GetHandler() http.Handler {
	return r.handler
}

// Close shuts down everything in the instance.
func (r *RelayEnterprise) Close() {
	if r.autoConfigStream != nil {
		r.autoConfigStream.Close()
	}
	r.core.Close()
}
