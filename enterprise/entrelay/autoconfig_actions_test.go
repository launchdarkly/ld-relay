package entrelay

import (
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	c "github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

// The tests in this file verify the auto-configuration behavior of RelayEnterprise, assuming that the
// low-level StreamManager implementation is working correctly. StreamManager is tested more thoroughly,
// including error conditions and reconnection, in the autoconfig package where it is implemented.
//
// These tests use a real HTTP server to provide the configuration stream, but they use FakeLDClient
// instead of creating real SDK clients, so there are no SDK connections made.

const testAutoConfKey = entconfig.AutoConfigKey("test-auto-conf-key")

var testAutoConfDefaultConfig = entconfig.EnterpriseConfig{
	AutoConfig: entconfig.AutoConfigConfig{Key: testAutoConfKey},
}

type autoConfTestParams struct {
	t                *testing.T
	relay            *RelayEnterprise
	stream           httphelpers.SSEStreamControl
	requestsCh       <-chan httphelpers.HTTPRequestInfo
	clientsCreatedCh <-chan *testclient.FakeLDClient
	mockLog          *ldlogtest.MockLog
}

func autoConfTest(
	t *testing.T,
	config entconfig.EnterpriseConfig,
	initialEvent *httphelpers.SSEEvent,
	action func(p autoConfTestParams),
) {
	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	streamHandler, stream := httphelpers.SSEHandler(initialEvent)
	defer stream.Close()
	handler, requestsCh := httphelpers.RecordingHandler(streamHandler)

	clientsCreatedCh := make(chan *testclient.FakeLDClient, 10)

	p := autoConfTestParams{
		t:                t,
		stream:           stream,
		requestsCh:       requestsCh,
		clientsCreatedCh: clientsCreatedCh,
		mockLog:          mockLog,
	}

	httphelpers.WithServer(handler, func(server *httptest.Server) {
		config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		relay, err := NewRelayEnterprise(
			config,
			mockLog.Loggers,
			testclient.FakeLDClientFactoryWithChannel(true, clientsCreatedCh),
		)
		if err != nil {
			panic(err)
		}
		p.relay = relay
		defer relay.Close()
		action(p)
	})
}

func (p autoConfTestParams) awaitClient() *testclient.FakeLDClient {
	select {
	case c := <-p.clientsCreatedCh:
		return c
	case <-time.After(time.Second):
		require.Fail(p.t, "timed out waiting for client creation")
		return nil
	}
}

func (p autoConfTestParams) shouldNotCreateClient(timeout time.Duration) {
	select {
	case <-p.clientsCreatedCh:
		require.Fail(p.t, "unexpectedly created client")
	case <-time.After(timeout):
		break
	}
}

func (p autoConfTestParams) awaitEnvironment(envID c.EnvironmentID) relayenv.EnvContext {
	var e relayenv.EnvContext
	require.Eventually(p.t, func() bool {
		e = p.relay.core.GetEnvironment(envID)
		return e != nil
	}, time.Second, time.Millisecond*5)
	return e
}

func (p autoConfTestParams) shouldNotHaveEnvironment(envID c.EnvironmentID, timeout time.Duration) {
	require.Eventually(p.t, func() bool { return p.relay.core.GetEnvironment(envID) == nil }, timeout, time.Millisecond*5)
}

func TestAutoConfigInit(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1, testAutoConfEnv2)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		client2 := p.awaitClient()
		if client1.Key == testAutoConfEnv2.sdkKey {
			client1, client2 = client2, client1
		}
		assert.Equal(t, testAutoConfEnv1.sdkKey, client1.Key)
		assert.Equal(t, testAutoConfEnv2.sdkKey, client2.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env1)
		assertEnvLookup(t, p.relay, env1, testAutoConfEnv1)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2, env2)
		assertEnvLookup(t, p.relay, env2, testAutoConfEnv2)
	})
}

func TestAutoConfigInitAfterPreviousInitCanAddAndRemoveEnvs(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv1.sdkKey, client1.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env1)
		assertEnvLookup(t, p.relay, env1, testAutoConfEnv1)

		p.stream.Enqueue(makeAutoConfPutEvent(testAutoConfEnv2))

		client2 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv2.sdkKey, client2.Key)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2, env2)
		assertEnvLookup(t, p.relay, env2, testAutoConfEnv2)

		client1.AwaitClose(t, time.Second)

		p.shouldNotHaveEnvironment(testAutoConfEnv1.id, time.Millisecond*100)
	})
}

func TestAutoConfigAddEnvironment(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv1.sdkKey, client1.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env1)

		p.stream.Enqueue(makeAutoConfPatchEvent(testAutoConfEnv2))

		client2 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv2.sdkKey, client2.Key)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvLookup(t, p.relay, env2, testAutoConfEnv2)
		assertEnvProps(t, testAutoConfEnv2, env2)
	})
}

func TestAutoConfigAddEnvironmentWithExpiringSDKKey(t *testing.T) {
	newKey := c.SDKKey("newsdkkey")
	oldKey := c.SDKKey("oldsdkkey")
	envWithKeys := testAutoConfEnv1
	envWithKeys.sdkKey = newKey
	envWithKeys.sdkKeyExpiryValue = oldKey
	envWithKeys.sdkKeyExpiryTime = ldtime.UnixMillisNow() + 100000

	initialEvent := makeAutoConfPatchEvent(envWithKeys)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		client2 := p.awaitClient()
		if client1.Key == oldKey {
			client1, client2 = client2, client1
		}
		assert.Equal(t, newKey, client1.Key)
		assert.Equal(t, oldKey, client2.Key)

		env := p.awaitEnvironment(envWithKeys.id)
		assertEnvProps(t, envWithKeys, env)

		expectedCredentials := credentialsAsSet(envWithKeys.id, envWithKeys.mobKey, envWithKeys.sdkKey)
		assert.Equal(t, expectedCredentials, credentialsAsSet(env.GetCredentials()...))

		select {
		case <-client2.CloseCh:
			require.Fail(t, "should not have closed client for deprecated key yet")
		case <-time.After(time.Millisecond * 300):
			break
		}
	})
}

func TestAutoConfigUpdateEnvironmentName(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env)

		modified := testAutoConfEnv1
		modified.envName = "newenvname"
		modified.projName = "newprojname"
		modified.version++

		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		p.shouldNotCreateClient(time.Millisecond * 50)

		nameChanged := func() bool { return env.GetName() == "newprojname newenvname" }
		require.Eventually(p.t, nameChanged, time.Second, time.Millisecond*5)
	})
}

func TestAutoConfigUpdateEnvironmentMobileKey(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env)

		modified := testAutoConfEnv1
		modified.mobKey = c.MobileKey("newmobkey")
		modified.version++

		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		p.shouldNotCreateClient(time.Millisecond * 50)

		expectedCredentials := credentialsAsSet(modified.id, modified.mobKey, modified.sdkKey)
		mobKeyChanged := func() bool {
			return reflect.DeepEqual(credentialsAsSet(env.GetCredentials()...), expectedCredentials)
		}
		require.Eventually(p.t, mobKeyChanged, time.Second, time.Millisecond*5)
		assertEnvLookup(t, p.relay, env, modified)
		assert.Nil(t, p.relay.core.GetEnvironment(testAutoConfEnv1.mobKey))
	})
}

func TestAutoConfigUpdateEnvironmentSDKKeyWithNoExpiry(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env)

		modified := testAutoConfEnv1
		modified.sdkKey = c.SDKKey("newsdkkey")
		modified.version++

		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.sdkKey, client2.Key)

		client1.AwaitClose(t, time.Second)

		expectedCredentials := credentialsAsSet(modified.id, modified.mobKey, modified.sdkKey)
		sdkKeyChanged := func() bool {
			return reflect.DeepEqual(credentialsAsSet(env.GetCredentials()...), expectedCredentials)
		}
		require.Eventually(p.t, sdkKeyChanged, time.Second, time.Millisecond*5)
		assertEnvLookup(t, p.relay, env, modified)
		assert.Nil(t, p.relay.core.GetEnvironment(testAutoConfEnv1.sdkKey))
	})
}

func TestAutoConfigUpdateEnvironmentSDKKeyWithExpiry(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env)

		modified := testAutoConfEnv1
		modified.sdkKey = c.SDKKey("newsdkkey")
		modified.sdkKeyExpiryValue = testAutoConfEnv1.sdkKey
		modified.sdkKeyExpiryTime = ldtime.UnixMillisNow() + 100000
		modified.version++

		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.sdkKey, client2.Key)

		expectedCredentials := credentialsAsSet(modified.id, modified.mobKey, modified.sdkKey)
		sdkKeyChanged := func() bool {
			return reflect.DeepEqual(credentialsAsSet(env.GetCredentials()...), expectedCredentials)
		}
		require.Eventually(p.t, sdkKeyChanged, time.Second, time.Millisecond*5)
		assertEnvLookup(t, p.relay, env, modified)
		assertEnvLookup(t, p.relay, env, testAutoConfEnv1) // looking up env by old key still works

		select {
		case <-client1.CloseCh:
			require.Fail(t, "should not have closed client for deprecated key yet")
		case <-time.After(time.Millisecond * 300):
			break
		}
	})
}

func TestAutoConfigRemovesCredentialForExpiredSDKKey(t *testing.T) {
	briefExpiryMillis := 300
	oldKey := testAutoConfEnv1.sdkKey
	newKey := c.SDKKey("newsdkkey")

	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)

	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env)

		modified := testAutoConfEnv1
		modified.sdkKey = newKey
		modified.sdkKeyExpiryValue = oldKey
		modified.sdkKeyExpiryTime = ldtime.UnixMillisNow() + ldtime.UnixMillisecondTime(briefExpiryMillis)
		modified.version++

		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, newKey, client2.Key)

		expectedCredentials := credentialsAsSet(modified.id, modified.mobKey, newKey)
		sdkKeyChanged := func() bool {
			return reflect.DeepEqual(credentialsAsSet(env.GetCredentials()...), expectedCredentials)
		}
		require.Eventually(p.t, sdkKeyChanged, time.Second, time.Millisecond*5)
		assertEnvLookup(t, p.relay, env, modified)
		assert.Equal(t, env, p.relay.core.GetEnvironment(oldKey))

		<-time.After(time.Duration(briefExpiryMillis+100) * time.Millisecond)

		select {
		case <-client1.CloseCh:
			break
		case <-time.After(time.Millisecond * 300):
			require.Fail(t, "timed out waiting for client with old key to close")
		}

		assert.Equal(t, expectedCredentials, credentialsAsSet(env.GetCredentials()...))
		assert.Nil(t, p.relay.core.GetEnvironment(oldKey))
	})
}

func TestAutoConfigDeleteEnvironment(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1, testAutoConfEnv2)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		client2 := p.awaitClient()
		if client1.Key == testAutoConfEnv2.sdkKey {
			client1, client2 = client2, client1
		}

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env1)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2, env2)

		p.stream.Enqueue(makeAutoConfDeleteEvent(testAutoConfEnv1.id, testAutoConfEnv1.version+1))

		client1.AwaitClose(t, time.Second)

		p.shouldNotHaveEnvironment(testAutoConfEnv1.id, time.Millisecond*100)
	})
}

func assertEnvLookup(t *testing.T, r *RelayEnterprise, env relayenv.EnvContext, te testAutoConfEnv) {
	assert.Equal(t, env, r.core.GetEnvironment(te.id))
	assert.Equal(t, env, r.core.GetEnvironment(te.mobKey))
	assert.Equal(t, env, r.core.GetEnvironment(te.sdkKey))
}

func assertEnvProps(t *testing.T, expected testAutoConfEnv, env relayenv.EnvContext) {
	assert.Equal(t, credentialsAsSet(expected.id, expected.mobKey, expected.sdkKey), credentialsAsSet(env.GetCredentials()...))
	assert.Equal(t, expected.projName+" "+expected.envName, env.GetName())
}

func credentialsAsSet(cs ...c.SDKCredential) map[c.SDKCredential]struct{} {
	ret := make(map[c.SDKCredential]struct{}, len(cs))
	for _, c := range cs {
		ret[c] = struct{}{}
	}
	return ret
}
