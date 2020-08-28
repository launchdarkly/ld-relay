package relayenv

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-configtypes"
	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/streams"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
)

const envName = "envname"

func requireEnvReady(t *testing.T, readyCh <-chan EnvContext) EnvContext {
	select {
	case e := <-readyCh:
		return e
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for environment")
		return nil
	}
}

func requireClientReady(t *testing.T, clientCh chan *testclient.FakeLDClient) *testclient.FakeLDClient {
	select {
	case c := <-clientCh:
		return c
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for client")
		return nil
	}
}

func makeBasicEnv(t *testing.T, envConfig config.EnvConfig, clientFactory sdks.ClientFactoryFunc,
	loggers ldlog.Loggers, readyCh chan EnvContext) EnvContext {
	env, err := NewEnvContext(
		EnvIdentifiers{ConfiguredName: envName},
		envConfig,
		config.Config{},
		clientFactory,
		ldcomponents.InMemoryDataStore(),
		[]streams.StreamProvider{},
		JSClientContext{},
		nil,
		"user-agent",
		LogNameIsSDKKey,
		loggers,
		readyCh,
	)
	require.NoError(t, err)
	return env
}

func TestConstructorBasicProperties(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, envName, env.GetIdentifiers().ConfiguredName)
	assert.Equal(t, time.Hour, env.GetTTL())
	assert.True(t, env.IsSecureMode())
	assert.Nil(t, env.GetEventDispatcher())                        // events were not enabled
	assert.Equal(t, context.Background(), env.GetMetricsContext()) // metrics aren't being used

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, envConfig.MobileKey)
	assert.Contains(t, creds, envConfig.EnvID)

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, env.GetClient(), requireClientReady(t, clientCh))
	assert.Nil(t, env.GetInitError())

	assert.NotNil(t, env.GetStore())
}

func TestConstructorWithOnlySDKKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, []config.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, env.GetClient(), requireClientReady(t, clientCh))
	assert.Nil(t, env.GetInitError())
}

func TestConstructorWithJSClientContext(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	jsClientContext := JSClientContext{Origins: []string{"origin"}}
	env, err := NewEnvContext(
		EnvIdentifiers{ConfiguredName: envName},
		envConfig,
		config.Config{},
		testclient.FakeLDClientFactory(true),
		ldcomponents.InMemoryDataStore(),
		[]streams.StreamProvider{},
		jsClientContext,
		nil,
		"user-agent",
		LogNameIsSDKKey,
		ldlog.NewDisabledLoggers(),
		nil,
	)
	require.NoError(t, err)
	defer env.Close()

	assert.Equal(t, jsClientContext, env.GetJSClientContext())
}

func TestLogPrefix(t *testing.T) {
	testPrefix := func(desc string, mode LogNameMode, sdkKey config.SDKKey, envID config.EnvironmentID, expected string) {
		t.Run(desc, func(t *testing.T) {
			envConfig := config.EnvConfig{SDKKey: sdkKey, EnvID: envID}
			mockLog := ldlogtest.NewMockLog()
			env, err := NewEnvContext(
				EnvIdentifiers{ConfiguredName: "name"},
				envConfig,
				config.Config{},
				testclient.FakeLDClientFactory(true),
				ldcomponents.InMemoryDataStore(),
				[]streams.StreamProvider{},
				JSClientContext{},
				nil,
				"user-agent",
				mode,
				mockLog.Loggers,
				nil,
			)
			require.NoError(t, err)
			defer env.Close()
			envImpl := env.(*envContextImpl)
			envImpl.loggers.Error("message")
			mockLog.AssertMessageMatch(t, true, ldlog.Error, "^"+regexp.QuoteMeta(expected)+" message")
		})
	}

	testPrefix("SDK key", LogNameIsSDKKey, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...7890]")
	testPrefix("env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...ghij]")
	testPrefix("env ID not set", LogNameIsEnvID, config.SDKKey("1234567890"), "", "[env: ...7890]")
	testPrefix("impossibly short SDK key", LogNameIsSDKKey, config.SDKKey("890"), config.EnvironmentID("abcdefghij"), "[env: 890]")
	testPrefix("impossibly short env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("hij"), "[env: hij]")
}

func TestAddRemoveCredential(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, nil)
	defer env.Close()

	assert.Equal(t, []config.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	env.AddCredential(st.EnvWithAllCredentials.Config.MobileKey)
	env.AddCredential(st.EnvWithAllCredentials.Config.EnvID)

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.EnvID)

	env.RemoveCredential(st.EnvWithAllCredentials.Config.MobileKey)

	creds = env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.EnvID)
}

func TestAddExistingCredentialDoesNothing(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, nil)
	defer env.Close()

	assert.Equal(t, []config.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	env.AddCredential(st.EnvWithAllCredentials.Config.MobileKey)

	creds := env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)

	env.AddCredential(st.EnvWithAllCredentials.Config.MobileKey)

	creds = env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)
}

func TestChangeSDKKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	newKey := config.SDKKey("new-key")

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	assert.Equal(t, env.GetClient(), client1)
	assert.Nil(t, env.GetInitError())

	env.AddCredential(newKey)
	env.DeprecateCredential(envConfig.SDKKey)

	assert.Equal(t, []config.SDKCredential{newKey}, env.GetCredentials())

	client2 := requireClientReady(t, clientCh)
	assert.NotEqual(t, client1, client2)
	assert.Equal(t, env.GetClient(), client2)

	select {
	case <-client1.CloseCh:
		require.Fail(t, "client for deprecated key should not have been closed")
	case <-time.After(time.Millisecond * 20):
		break
	}

	env.RemoveCredential(envConfig.SDKKey)

	assert.Equal(t, []config.SDKCredential{newKey}, env.GetCredentials())

	client1.AwaitClose(t, time.Millisecond*20)
}

func TestSDKClientCreationFails(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	fakeError := errors.New("sorry")

	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	env := makeBasicEnv(t, envConfig, testclient.ClientFactoryThatFails(fakeError), mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, fakeError, env.GetInitError())
	assert.Nil(t, env.GetStore())
}

func TestDisplayName(t *testing.T) {
	ei1 := EnvIdentifiers{ProjName: "a", EnvName: "b", ConfiguredName: "thing"}
	assert.Equal(t, "thing", ei1.GetDisplayName())

	ei2 := EnvIdentifiers{ProjName: "a", EnvName: "b"}
	assert.Equal(t, "a b", ei2.GetDisplayName())
}
