package entrelay

import (
	"errors"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"
	"github.com/launchdarkly/ld-relay/v6/enterprise/version"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

var (
	errNoEnvironments = errors.New("you must specify at least one environment in your configuration if you are not using auto-configuration")
)

// RelayEnterprise is the main object for Relay Proxy Enterprise. Most of its functionality comes from RelayCore.
type RelayEnterprise struct {
	core    *core.RelayCore
	config  entconfig.EnterpriseConfig
	handler http.Handler
}

// NewRelayEnterprise creates a new RelayEnterprise instance.
func NewRelayEnterprise(
	c entconfig.EnterpriseConfig,
	loggers ldlog.Loggers,
	clientFactory sdks.ClientFactoryFunc,
) (*RelayEnterprise, error) {
	userAgent := "LDRelayEnterprise/" + version.Version

	hasAutoConfigKey := c.AutoConfig.Key != ""

	if !hasAutoConfigKey && len(c.Environment) == 0 {
		return nil, errNoEnvironments
	}

	core, err := core.NewRelayCore(
		c.Config,
		loggers,
		clientFactory,
		version.Version,
		userAgent,
	)
	if err != nil {
		return nil, err
	}

	r := &RelayEnterprise{
		core:   core,
		config: c,
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
	r.core.Close()
}
