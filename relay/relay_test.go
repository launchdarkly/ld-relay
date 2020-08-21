package relay

import (
	"testing"

	c "github.com/launchdarkly/ld-relay-config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRelayRejectsConfigWithNoEnvironments(t *testing.T) {
	config := c.Config{}
	relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "you must specify at least one environment")
	assert.Nil(t, relay)
}
