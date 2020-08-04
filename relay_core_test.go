package relay

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

func TestNewRelayCoreRejectsConfigWithContradictoryProperties(t *testing.T) {
	// it is an error to enable TLS but not provide a cert or key
	config := c.Config{Main: c.MainConfig{TLSEnabled: true}}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), fakeLDClientFactory(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS cert")
	assert.Nil(t, core)
}

func TestNewRelayCoreRejectsConfigWithNoEnvironments(t *testing.T) {
	config := c.Config{}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), fakeLDClientFactory(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "you must specify at least one environment")
	assert.Nil(t, core)
}

func TestRelayCoreGetEnvironment(t *testing.T) {
	config := c.Config{
		Environment: makeEnvConfigs(testEnvMain, testEnvMobile, testEnvClientSide),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), fakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	if assert.NotNil(t, core.GetEnvironment(testEnvMain.config.SDKKey)) {
		assert.Equal(t, testEnvMain.name, core.GetEnvironment(testEnvMain.config.SDKKey).GetName())
	}
	if assert.NotNil(t, core.GetEnvironment(testEnvMobile.config.SDKKey)) {
		assert.Equal(t, testEnvMobile.name, core.GetEnvironment(testEnvMobile.config.SDKKey).GetName())
	}
	if assert.NotNil(t, core.GetEnvironment(testEnvClientSide.config.SDKKey)) {
		assert.Equal(t, testEnvClientSide.name, core.GetEnvironment(testEnvClientSide.config.SDKKey).GetName())
	}

	if assert.NotNil(t, core.GetEnvironment(testEnvMobile.config.MobileKey)) {
		assert.Equal(t, testEnvMobile.name, core.GetEnvironment(testEnvMobile.config.MobileKey).GetName())
	}

	if assert.NotNil(t, core.GetEnvironment(testEnvClientSide.config.EnvID)) {
		assert.Equal(t, testEnvClientSide.name, core.GetEnvironment(testEnvClientSide.config.EnvID).GetName())
	}

	assert.Nil(t, core.GetEnvironment(undefinedSDKKey))

	assert.Nil(t, core.GetEnvironment(unsupportedSDKCredential{}))
}

func TestRelayCoreGetAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: makeEnvConfigs(testEnvMain, testEnvMobile, testEnvClientSide),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), fakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	envs := core.GetAllEnvironments()
	assert.Len(t, envs, 3)
	if assert.NotNil(t, envs[testEnvMain.config.SDKKey]) {
		assert.Equal(t, testEnvMain.name, envs[testEnvMain.config.SDKKey].GetName())
	}
	if assert.NotNil(t, envs[testEnvMobile.config.SDKKey]) {
		assert.Equal(t, testEnvMobile.name, envs[testEnvMobile.config.SDKKey].GetName())
	}
	if assert.NotNil(t, envs[testEnvClientSide.config.SDKKey]) {
		assert.Equal(t, testEnvClientSide.name, envs[testEnvClientSide.config.SDKKey].GetName())
	}
}

func TestRelayCoreWaitForAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: makeEnvConfigs(testEnvMain, testEnvMobile),
	}

	t.Run("returns nil if all environments initialize successfully", func(t *testing.T) {
		core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), fakeLDClientFactory(true))
		require.NoError(t, err)
		defer core.Close()

		err = core.WaitForAllClients(time.Second)
		assert.NoError(t, err)
	})

	t.Run("returns error if any environment does not initialize successfully", func(t *testing.T) {
		oneEnvFails := func(sdkKey c.SDKKey, config ld.Config) (sdkconfig.LDClientContext, error) {
			shouldFail := sdkKey == testEnvMobile.config.SDKKey
			if shouldFail {
				return clientFactoryThatFails(errors.New("sorry"))(sdkKey, config)
			}
			return fakeLDClientFactory(true)(sdkKey, config)
		}
		core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), oneEnvFails)
		require.NoError(t, err)
		defer core.Close()

		err = core.WaitForAllClients(time.Second)
		assert.Error(t, err)
	})
}
