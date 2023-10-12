package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeBasicRelay(config c.Config) (*Relay, error) {
	return newRelayInternal(config, relayInternalOptions{
		clientFactory: testclient.FakeLDClientFactory(true),
		loggers:       ldlog.NewDisabledLoggers(),
	})
}

func TestNewRelayCoreRejectsConfigWithContradictoryProperties(t *testing.T) {
	// it is an error to enable TLS but not provide a cert or key
	config := c.Config{Main: c.MainConfig{TLSEnabled: true}}
	relay, err := makeBasicRelay(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS cert")
	assert.Nil(t, relay)
}

func TestRelayGetEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile, st.EnvClientSide),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	env, err := relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))
	require.NotNil(t, env)
	assert.Nil(t, err)
	assert.Equal(t, st.EnvMain.Name, env.GetIdentifiers().ConfiguredName)

	env, err = relay.getEnvironment(sdkauth.New(st.EnvMobile.Config.SDKKey))
	require.NotNil(t, env)
	assert.Nil(t, err)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	env, err = relay.getEnvironment(sdkauth.New(st.EnvClientSide.Config.SDKKey))
	require.NotNil(t, env)
	assert.Nil(t, err)
	assert.Equal(t, st.EnvClientSide.Name, env.GetIdentifiers().ConfiguredName)

	env, err = relay.getEnvironment(sdkauth.New(st.EnvMobile.Config.MobileKey))
	require.NotNil(t, env)
	assert.Nil(t, err)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	env, err = relay.getEnvironment(sdkauth.New(st.EnvClientSide.Config.EnvID))
	require.NotNil(t, env)
	assert.Nil(t, err)
	assert.Equal(t, st.EnvClientSide.Name, env.GetIdentifiers().ConfiguredName)

	env, err = relay.getEnvironment(sdkauth.New(st.UndefinedSDKKey))
	assert.Nil(t, env)
	assert.True(t, IsUnrecognizedEnvironment(err))

	env, err = relay.getEnvironment(sdkauth.New(st.UnsupportedSDKCredential{}))
	assert.Nil(t, env)
	assert.True(t, IsUnrecognizedEnvironment(err))

	env, err = relay.getEnvironment(sdkauth.NewScoped("nonexistent-filter", st.EnvMain.Config.SDKKey))
	assert.Nil(t, env)
	assert.True(t, IsPayloadFilterNotFound(err))
}

func TestRelayGetAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile, st.EnvClientSide),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	envs := relay.getAllEnvironments()
	assert.Len(t, envs, 3)
	var names []string
	for _, e := range envs {
		names = append(names, e.GetIdentifiers().ConfiguredName)
	}
	assert.Contains(t, names, st.EnvMain.Name)
	assert.Contains(t, names, st.EnvMobile.Name)
	assert.Contains(t, names, st.EnvClientSide.Name)
}

func TestRelayAddEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	env, resultCh, err := relay.addEnvironment(relayenv.EnvIdentifiers{ConfiguredName: st.EnvMobile.Name}, st.EnvMobile.Config, nil)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, resultCh)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	env1, _ := relay.getEnvironment(sdkauth.New(st.EnvMobile.Config.SDKKey))
	assert.Equal(t, env, env1)

	env2 := helpers.RequireValue(t, resultCh, time.Second, "timed out waiting for new environment to initialize")
	assert.Equal(t, env, env2)
}

func TestRelayAddEnvironmentAfterClosed(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	_ = relay.Close()

	env, resultCh, err := relay.addEnvironment(relayenv.EnvIdentifiers{ConfiguredName: st.EnvMobile.Name}, st.EnvMobile.Config, nil)
	assert.Error(t, err)
	assert.Nil(t, env)
	assert.Nil(t, resultCh)
}

func TestRelayRemoveEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	env, _ := relay.getEnvironment(sdkauth.New(st.EnvMobile.Config.SDKKey))
	require.NotNil(t, env)
	assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)

	relay.removeEnvironment(sdkauth.New(st.EnvMobile.Config.SDKKey))

	noEnv, _ := relay.getEnvironment(sdkauth.New(st.EnvMobile.Config.SDKKey))
	assert.Nil(t, noEnv)
}

func TestRelayRemoveUnknownEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	relay.removeEnvironment(sdkauth.New(c.EnvironmentID("unknown")))
	// just shows that it doesn't panic or anything
}

func TestRelayAddedEnvironmentCredential(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	env, _ := relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))
	require.NotNil(t, env)
	assert.Equal(t, st.EnvMain.Name, env.GetIdentifiers().ConfiguredName)

	extraKey := c.SDKKey(string(st.EnvMain.Config.SDKKey) + "-extra")
	noEnv, _ := relay.getEnvironment(sdkauth.New(extraKey))
	assert.Nil(t, noEnv)

	relay.addConnectionMapping(sdkauth.New(extraKey), env)

	env1, _ := relay.getEnvironment(sdkauth.New(extraKey))
	assert.Equal(t, env, env1)
}

func TestRelayRemovingEnvironmentCredential(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	relay, err := makeBasicRelay(config)
	require.NoError(t, err)
	defer relay.Close()

	relay.removeConnectionMapping(sdkauth.New(st.EnvMain.Config.SDKKey))

	_, err = relay.getEnvironment(sdkauth.New(st.EnvMain.Config.SDKKey))
	assert.Error(t, err)

	env, err := relay.getEnvironment(sdkauth.New(st.EnvMobile.Config.SDKKey))
	if assert.NoError(t, err) {
		assert.NotNil(t, env)
		assert.Equal(t, st.EnvMobile.Name, env.GetIdentifiers().ConfiguredName)
	}

	envs := relay.getAllEnvironments()
	assert.Equal(t, len(envs), 2) // EnvMain is not removed from this list
}

func TestRelayWaitForAllEnvironments(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}

	t.Run("returns nil if all environments initialize successfully", func(t *testing.T) {
		relay, err := makeBasicRelay(config)
		require.NoError(t, err)
		defer relay.Close()

		err = relay.waitForAllClients(time.Second)
		assert.NoError(t, err)
	})

	t.Run("returns error if any environment does not initialize successfully", func(t *testing.T) {
		relay, err := newRelayInternal(config, relayInternalOptions{
			clientFactory: oneEnvFails(st.EnvMobile.Config.SDKKey, false, nil),
			loggers:       ldlog.NewDisabledLoggers(),
		})
		require.NoError(t, err)
		defer relay.Close()

		err = relay.waitForAllClients(time.Second)
		assert.Error(t, err)
	})
}

func TestRelayUninitializedEnvironment(t *testing.T) {
	config := c.Config{
		Environment: st.MakeEnvConfigs(st.EnvMain, st.EnvMobile),
	}
	problemEnv := st.EnvMobile

	t.Run("handlers return 503 for environment that is still initializing", func(t *testing.T) {
		gateCh := make(chan struct{})
		defer close(gateCh)

		relay, err := newRelayInternal(config, relayInternalOptions{
			clientFactory: oneEnvFails(st.EnvMobile.Config.SDKKey, true, gateCh),
			loggers:       ldlog.NewDisabledLoggers(),
		})
		require.NoError(t, err)
		defer relay.Close()
		router := relay.makeRouter()

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
		relay, err := newRelayInternal(config, relayInternalOptions{
			clientFactory: oneEnvFails(st.EnvMobile.Config.SDKKey, true, nil),
			loggers:       ldlog.NewDisabledLoggers(),
		})
		require.NoError(t, err)
		defer relay.Close()
		router := relay.makeRouter()

		err = relay.waitForAllClients(time.Millisecond * 100)
		assert.Error(t, err)

		env, _ := relay.getEnvironment(sdkauth.New(problemEnv.Config.SDKKey))
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
