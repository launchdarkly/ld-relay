package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testenv"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	env, inited := core.GetEnvironment(st.EnvMain.Config.SDKKey)
	require.NotNil(t, env)
	assert.True(t, inited)
	assert.Equal(t, st.EnvMain.Name, env.GetIdentifiers().ConfiguredName)

	env, inited = core.GetEnvironment(st.EnvMobile.Config.SDKKey)
	require.NotNil(t, env)
	assert.True(t, inited)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	env, inited = core.GetEnvironment(st.EnvClientSide.Config.SDKKey)
	require.NotNil(t, env)
	assert.True(t, inited)
	assert.Equal(t, st.EnvClientSide.Name, env.GetIdentifiers().ConfiguredName)

	env, inited = core.GetEnvironment(st.EnvMobile.Config.MobileKey)
	require.NotNil(t, env)
	assert.True(t, inited)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	env, inited = core.GetEnvironment(st.EnvClientSide.Config.EnvID)
	require.NotNil(t, env)
	assert.True(t, inited)
	assert.Equal(t, st.EnvClientSide.Name, env.GetIdentifiers().ConfiguredName)

	env, inited = core.GetEnvironment(st.UndefinedSDKKey)
	assert.Nil(t, env)
	assert.True(t, inited)

	env, inited = core.GetEnvironment(st.UnsupportedSDKCredential{})
	assert.Nil(t, env)
	assert.True(t, inited)
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

	env, resultCh, err := core.AddEnvironment(relayenv.EnvIdentifiers{ConfiguredName: st.EnvMobile.Name}, st.EnvMobile.Config, nil)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, resultCh)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	env1, _ := core.GetEnvironment(st.EnvMobile.Config.SDKKey)
	assert.Equal(t, env, env1)

	select {
	case env2 := <-resultCh:
		assert.Equal(t, env, env2)
	case <-time.After(time.Second):
		assert.Fail(t, "timed out waiting for new environment to initialize")
	}
}

func TestRelayCoreAddEnvironmentAfterClosed(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	core.Close()

	env, resultCh, err := core.AddEnvironment(relayenv.EnvIdentifiers{ConfiguredName: st.EnvMobile.Name}, st.EnvMobile.Config, nil)
	assert.Error(t, err)
	assert.Nil(t, env)
	assert.Nil(t, resultCh)
}

func TestRelayCoreRemoveEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	env, _ := core.GetEnvironment(st.EnvMobile.Config.SDKKey)
	require.NotNil(t, env)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	removed := core.RemoveEnvironment(env)
	assert.True(t, removed)

	noEnv, _ := core.GetEnvironment(st.EnvMobile.Config.SDKKey)
	assert.Nil(t, noEnv)
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

func TestRelayCoreAddedEnvironmentCredential(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	env, _ := core.GetEnvironment(st.EnvMain.Config.SDKKey)
	require.NotNil(t, env)
	assert.Equal(t, st.EnvMain.Name, env.GetIdentifiers().ConfiguredName)

	extraKey := c.SDKKey(string(st.EnvMain.Config.SDKKey) + "-extra")
	noEnv, _ := core.GetEnvironment(extraKey)
	assert.Nil(t, noEnv)

	core.AddedEnvironmentCredential(env, extraKey)

	env1, _ := core.GetEnvironment(extraKey)
	assert.Equal(t, env, env1)
}

func TestRelayCoreRemovingEnvironmentCredential(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	core, err := makeBasicCore(config)
	require.NoError(t, err)
	defer core.Close()

	core.RemovingEnvironmentCredential(st.EnvMain.Config.SDKKey)

	noEnv, _ := core.GetEnvironment(st.EnvMain.Config.SDKKey)
	assert.Nil(t, noEnv)

	env, _ := core.GetEnvironment(st.EnvMobile.Config.SDKKey)
	require.NotNil(t, env)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	assert.Len(t, core.GetAllEnvironments(), 2) // EnvMain is not removed from this list
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
		core, err := NewRelayCore(config, ldlog.NewDisabledLoggers(),
			oneEnvFails(st.EnvMobile.Config.SDKKey, false, nil), "", "", false)
		require.NoError(t, err)
		defer core.Close()

		err = core.WaitForAllClients(time.Second)
		assert.Error(t, err)
	})
}

func TestRelayCoreUninitializedEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	problemEnv := st.EnvMobile

	t.Run("handlers return 503 for environment that is still initializing", func(t *testing.T) {
		gateCh := make(chan struct{})
		defer close(gateCh)

		core, err := NewRelayCore(config, ldlog.NewDisabledLoggers(),
			oneEnvFails(problemEnv.Config.SDKKey, true, gateCh), "", "", false)
		require.NoError(t, err)
		defer core.Close()
		router := core.MakeRouter()

		req1 := st.MakeSDKStreamEndpointRequest("", basictypes.ServerSideStream, problemEnv, st.SimpleUserJSON, 0)
		rr1 := httptest.NewRecorder()
		router.ServeHTTP(rr1, req1)
		assert.Equal(t, http.StatusServiceUnavailable, rr1.Result().StatusCode)

		req2 := st.MakeSDKStreamEndpointRequest("", basictypes.MobilePingStream, problemEnv, st.SimpleUserJSON, 0)
		rr2 := httptest.NewRecorder()
		router.ServeHTTP(rr2, req2)
		assert.Equal(t, http.StatusServiceUnavailable, rr2.Result().StatusCode)
	})

	t.Run("handlers accept requests for environment that failed to initialize", func(t *testing.T) {
		core, err := NewRelayCore(config, ldlog.NewDisabledLoggers(),
			oneEnvFails(problemEnv.Config.SDKKey, true, nil), "", "", false)
		require.NoError(t, err)
		defer core.Close()
		router := core.MakeRouter()

		err = core.WaitForAllClients(time.Millisecond * 100)
		assert.Error(t, err)

		env, _ := core.GetEnvironment(problemEnv.Config.SDKKey)
		assert.NotNil(t, env)
		store := env.GetStore()
		assert.NotNil(t, store)
		store.Init(nil)

		req1 := st.MakeSDKStreamEndpointRequest("", basictypes.ServerSideStream, problemEnv, "", 0)
		resp1 := st.WithStreamRequest(t, req1, router, func(ch <-chan eventsource.Event) { <-ch })
		assert.Equal(t, http.StatusOK, resp1.StatusCode)

		req2 := st.MakeSDKStreamEndpointRequest("", basictypes.MobilePingStream, problemEnv, st.SimpleUserJSON, 0)
		resp2 := st.WithStreamRequest(t, req2, router, func(ch <-chan eventsource.Event) { <-ch })
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
	})
}

func oneEnvFails(
	badSDKKey c.SDKKey,
	returnClientInstanceAnyway bool,
	gateCh <-chan struct{},
) sdks.ClientFactoryFunc {
	return func(sdkKey c.SDKKey, config ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		client, _ := testclient.FakeLDClientFactory(true)(sdkKey, config, timeout)
		if sdkKey == badSDKKey {
			if gateCh != nil {
				<-gateCh
			}
			err := errors.New("sorry")
			if returnClientInstanceAnyway {
				return client, err
			}
			return nil, err
		}
		return client, nil
	}
}
