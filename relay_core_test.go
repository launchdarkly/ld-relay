package relay

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

func TestNewRelayCoreRejectsConfigWithContradictoryProperties(t *testing.T) {
	// it is an error to enable TLS but not provide a cert or key
	config := c.Config{Main: c.MainConfig{TLSEnabled: true}}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS cert")
	assert.Nil(t, core)
}

func TestNewRelayCoreRejectsConfigWithNoEnvironments(t *testing.T) {
	config := c.Config{}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "you must specify at least one environment")
	assert.Nil(t, core)
}

func TestRelayCoreGetEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile, st.EnvClientSide),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	if assert.NotNil(t, core.GetEnvironment(st.EnvMain.Config.SDKKey)) {
		assert.Equal(t, st.EnvMain.Name, core.GetEnvironment(st.EnvMain.Config.SDKKey).GetName())
	}
	if assert.NotNil(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey)) {
		assert.Equal(t, st.EnvMobile.Name, core.GetEnvironment(st.EnvMobile.Config.SDKKey).GetName())
	}
	if assert.NotNil(t, core.GetEnvironment(st.EnvClientSide.Config.SDKKey)) {
		assert.Equal(t, st.EnvClientSide.Name, core.GetEnvironment(st.EnvClientSide.Config.SDKKey).GetName())
	}

	if assert.NotNil(t, core.GetEnvironment(st.EnvMobile.Config.MobileKey)) {
		assert.Equal(t, st.EnvMobile.Name, core.GetEnvironment(st.EnvMobile.Config.MobileKey).GetName())
	}

	if assert.NotNil(t, core.GetEnvironment(st.EnvClientSide.Config.EnvID)) {
		assert.Equal(t, st.EnvClientSide.Name, core.GetEnvironment(st.EnvClientSide.Config.EnvID).GetName())
	}

	assert.Nil(t, core.GetEnvironment(st.UndefinedSDKKey))

	assert.Nil(t, core.GetEnvironment(st.UnsupportedSDKCredential{}))
}

func TestRelayCoreGetAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile, st.EnvClientSide),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	envs := core.GetAllEnvironments()
	assert.Len(t, envs, 3)
	if assert.NotNil(t, envs[st.EnvMain.Config.SDKKey]) {
		assert.Equal(t, st.EnvMain.Name, envs[st.EnvMain.Config.SDKKey].GetName())
	}
	if assert.NotNil(t, envs[st.EnvMobile.Config.SDKKey]) {
		assert.Equal(t, st.EnvMobile.Name, envs[st.EnvMobile.Config.SDKKey].GetName())
	}
	if assert.NotNil(t, envs[st.EnvClientSide.Config.SDKKey]) {
		assert.Equal(t, st.EnvClientSide.Name, envs[st.EnvClientSide.Config.SDKKey].GetName())
	}
}

func TestRelayCoreAddEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	env, resultCh, err := core.AddEnvironment(st.EnvMobile.Name, st.EnvMobile.Config)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, resultCh)
	assert.Equal(t, st.EnvMobile.Name, env.GetName())

	if assert.NotNil(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey)) {
		assert.Equal(t, env, core.GetEnvironment(st.EnvMobile.Config.SDKKey))
	}

	select {
	case env := <-resultCh:
		assert.Equal(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey), env)
	case <-time.After(time.Second):
		assert.Fail(t, "timed out waiting for new environment to initialize")
	}
}

func TestRelayCoreRemoveEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	if assert.NotNil(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey)) {
		assert.Equal(t, st.EnvMobile.Name, core.GetEnvironment(st.EnvMobile.Config.SDKKey).GetName())
	}

	removed := core.RemoveEnvironment(st.EnvMobile.Config.SDKKey)
	assert.True(t, removed)

	assert.Nil(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey))
}

func TestRelayCoreRemoveUnknownEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
	require.NoError(t, err)
	defer core.Close()

	assert.False(t, core.RemoveEnvironment(st.EnvMobile.Config.SDKKey))
}

func TestRelayCoreWaitForAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}

	t.Run("returns nil if all environments initialize successfully", func(t *testing.T) {
		core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), testclient.FakeLDClientFactory(true))
		require.NoError(t, err)
		defer core.Close()

		err = core.WaitForAllClients(time.Second)
		assert.NoError(t, err)
	})

	t.Run("returns error if any environment does not initialize successfully", func(t *testing.T) {
		oneEnvFails := func(sdkKey c.SDKKey, config ld.Config) (sdks.LDClientContext, error) {
			shouldFail := sdkKey == st.EnvMobile.Config.SDKKey
			if shouldFail {
				return testclient.ClientFactoryThatFails(errors.New("sorry"))(sdkKey, config)
			}
			return testclient.FakeLDClientFactory(true)(sdkKey, config)
		}
		core, err := NewRelayCore(config, ldlog.NewDefaultLoggers(), oneEnvFails)
		require.NoError(t, err)
		defer core.Close()

		err = core.WaitForAllClients(time.Second)
		assert.Error(t, err)
	})
}
