package relay

import (
	"net/http/httptest"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/config"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRelayRejectsConfigWithNoEnvironmentsInManualConfigMode(t *testing.T) {
	config := c.Config{}
	relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
	assert.Equal(t, errNoEnvironments, err)
	assert.Nil(t, relay)
}

func TestNewRelayAllowsConfigWithNoEnvironmentsIfAutoConfigKeyIsSet(t *testing.T) {
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
		relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
		require.NoError(t, err)
		defer relay.Close()
	})
}

func TestNewRelayAllowsConfigWithNoEnvironmentsIfFileDataSourceIsSet(t *testing.T) {
	config := c.Config{
		OfflineMode: c.OfflineModeConfig{
			FileDataSource: "x",
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)

	// There will be an error, since we don't actually have a data file, but it should not be a
	// configuration error.
	require.Error(t, err)
	assert.NotEqual(t, errNoEnvironments, err)
}
