package core

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testenv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	c "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

func makeBasicCore(config c.Config) (*RelayCore, error) {
	return NewRelayCore(config, ldlog.NewDisabledLoggers(), testclient.FakeLDClientFactory(true), "", "", false)
}

func TestNewRelayCoreRejectsConfigWithContradictoryProperties(t *testing.T) {
	// it is an error to enable TLS but not provide a cert or key
	config := c.Config{Main: c.MainConfig{TLSEnabled: true}}
	core, err := makeBasicCore(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS cert")
	assert.Nil(t, core)
}

func TestRelayCoreGetEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile, st.EnvClientSide),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	if assert.NotNil(t, core.GetEnvironment(st.EnvMain.Config.SDKKey)) {
		assert.Equal(t, st.EnvMain.Name, core.GetEnvironment(st.EnvMain.Config.SDKKey).GetIdentifiers().ConfiguredName)
	}
	if assert.NotNil(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey)) {
		assert.Equal(t, st.EnvMobile.Name, core.GetEnvironment(st.EnvMobile.Config.SDKKey).GetIdentifiers().ConfiguredName)
	}
	if assert.NotNil(t, core.GetEnvironment(st.EnvClientSide.Config.SDKKey)) {
		assert.Equal(t, st.EnvClientSide.Name, core.GetEnvironment(st.EnvClientSide.Config.SDKKey).GetIdentifiers().ConfiguredName)
	}

	if assert.NotNil(t, core.GetEnvironment(st.EnvMobile.Config.MobileKey)) {
		assert.Equal(t, st.EnvMobile.Name, core.GetEnvironment(st.EnvMobile.Config.MobileKey).GetIdentifiers().ConfiguredName)
	}

	if assert.NotNil(t, core.GetEnvironment(st.EnvClientSide.Config.EnvID)) {
		assert.Equal(t, st.EnvClientSide.Name, core.GetEnvironment(st.EnvClientSide.Config.EnvID).GetIdentifiers().ConfiguredName)
	}

	assert.Nil(t, core.GetEnvironment(st.UndefinedSDKKey))

	assert.Nil(t, core.GetEnvironment(st.UnsupportedSDKCredential{}))
}

func TestRelayCoreGetAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile, st.EnvClientSide),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	envs := core.GetAllEnvironments()
	assert.Len(t, envs, 3)
	var names []string
	for _, e := range envs {
		names = append(names, e.GetIdentifiers().ConfiguredName)
	}
	assert.Contains(t, names, st.EnvMain.Name)
	assert.Contains(t, names, st.EnvMobile.Name)
	assert.Contains(t, names, st.EnvClientSide.Name)
}

func TestRelayCoreAddEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	env, resultCh, err := core.AddEnvironment(relayenv.EnvIdentifiers{ConfiguredName: st.EnvMobile.Name}, st.EnvMobile.Config)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, resultCh)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

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
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	env := core.GetEnvironment(st.EnvMobile.Config.SDKKey)
	require.NotNil(t, env)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	removed := core.RemoveEnvironment(env)
	assert.True(t, removed)

	assert.Nil(t, core.GetEnvironment(st.EnvMobile.Config.SDKKey))
}

func TestRelayCoreRemoveUnknownEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	env := testenv.NewTestEnvContext("unknown", true, st.NewInMemoryStore())

	assert.False(t, core.RemoveEnvironment(env))
}

func TestRelayCoreWaitForAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}

	t.Run("returns nil if all environments initialize successfully", func(t *testing.T) {
		core, err := NewRelayCore(config, ldlog.NewDisabledLoggers(), testclient.FakeLDClientFactory(true), "", "", false)
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
		core, err := NewRelayCore(config, ldlog.NewDisabledLoggers(), oneEnvFails, "", "", false)
		require.NoError(t, err)
		defer core.Close()

		err = core.WaitForAllClients(time.Second)
		assert.Error(t, err)
	})
}
