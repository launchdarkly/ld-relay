package entrelay

import (
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
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
	core, err := core.NewRelayCore(c.Config, loggers, clientFactory)
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
