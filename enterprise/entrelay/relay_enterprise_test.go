package entrelay

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	c "github.com/launchdarkly/ld-relay/v6/config"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestNewRelayEnterpriseRejectsConfigWithNoEnvironmentsAndNoAutoConfigKey(t *testing.T) {
	config := c.Config{}
	relay, err := NewRelayEnterprise(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "you must specify at least one environment")
	assert.Nil(t, relay)
}

func TestNewRelayEnterpriseAllowsConfigWithNoEnvironmentsIfAutoConfigKeyIsSet(t *testing.T) {
	stubStreamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()
	httphelpers.WithServer(stubStreamHandler, func(server *httptest.Server) {
		streamURI, _ := configtypes.NewOptURLAbsoluteFromString(server.URL)
		config := c.Config{
			Main: c.MainConfig{
				StreamURI: streamURI,
			},
			AutoConfig: c.AutoConfigConfig{
				Key: "x",
			},
		}
		relay, err := NewRelayEnterprise(config, ldlog.NewDisabledLoggers(), nil)
		require.NoError(t, err)
		defer relay.Close()
	})
}
