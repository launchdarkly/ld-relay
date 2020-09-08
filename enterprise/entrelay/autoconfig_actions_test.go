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
	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
)

// The tests in this file verify the auto-configuration behavior of RelayEnterprise, assuming that the
// low-level StreamManager implementation is working correctly. StreamManager is tested more thoroughly,
// including error conditions and reconnection, in the autoconfig package where it is implemented.
//
// These tests use a real HTTP server to provide the configuration stream, but they use FakeLDClient
// instead of creating real SDK clients, so there are no SDK connections made.

type autoConfTestParams struct {
	t                *testing.T
	relay            *RelayEnterprise
	stream           httphelpers.SSEStreamControl
	streamRequestsCh <-chan httphelpers.HTTPRequestInfo
	eventRequestsCh  <-chan httphelpers.HTTPRequestInfo
	clientsCreatedCh <-chan *testclient.FakeLDClient
	mockLog          *ldlogtest.MockLog
}

func autoConfTest(
	t *testing.T,
	config c.Config,
	initialEvent *httphelpers.SSEEvent,
	action func(p autoConfTestParams),
) {
	mockLog := ldlogtest.NewMockLog()
	defer sharedtest.DumpLogIfTestFailed(t, mockLog)

	streamHandler, stream := httphelpers.SSEHandler(initialEvent)
	defer stream.Close()
	streamRequestsHandler, streamRequestsCh := httphelpers.RecordingHandler(streamHandler)

	eventRequestsHandler, eventRequestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))

	clientsCreatedCh := make(chan *testclient.FakeLDClient, 10)

	p := autoConfTestParams{
		t:                t,
		stream:           stream,
		streamRequestsCh: streamRequestsCh,
		eventRequestsCh:  eventRequestsCh,
		clientsCreatedCh: clientsCreatedCh,
		mockLog:          mockLog,
	}

	httphelpers.WithServer(streamRequestsHandler, func(streamServer *httptest.Server) {
		httphelpers.WithServer(eventRequestsHandler, func(eventsServer *httptest.Server) {
			config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
			config.Events.SendEvents = true
			config.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(eventsServer.URL)
			config.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)

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

func (p autoConfTestParams) assertEnvLookup(env relayenv.EnvContext, te testAutoConfEnv) {
	assert.Equal(p.t, env, p.relay.core.GetEnvironment(te.id))
	assert.Equal(p.t, env, p.relay.core.GetEnvironment(te.mobKey))
	assert.Equal(p.t, env, p.relay.core.GetEnvironment(te.sdkKey))
}

func (p autoConfTestParams) awaitCredentialsUpdated(env relayenv.EnvContext, expected testAutoConfEnv) {
	expectedCredentials := credentialsAsSet(expected.id, expected.mobKey, expected.sdkKey)
	isChanged := func() bool {
		return reflect.DeepEqual(credentialsAsSet(env.GetCredentials()...), expectedCredentials)
	}
	require.Eventually(p.t, isChanged, time.Second, time.Millisecond*5)
	p.assertEnvLookup(env, expected)
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
		p.assertEnvLookup(env1, testAutoConfEnv1)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2, env2)
		p.assertEnvLookup(env2, testAutoConfEnv2)
	})
}

func TestAutoConfigInitAfterPreviousInitCanAddAndRemoveEnvs(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv1.sdkKey, client1.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1, env1)
		p.assertEnvLookup(env1, testAutoConfEnv1)

		p.stream.Enqueue(makeAutoConfPutEvent(testAutoConfEnv2))

		client2 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv2.sdkKey, client2.Key)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2, env2)
		p.assertEnvLookup(env2, testAutoConfEnv2)

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
		p.assertEnvLookup(env2, testAutoConfEnv2)
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

		nameChanged := func() bool { return env.GetIdentifiers().GetDisplayName() == "newprojname newenvname" }
		require.Eventually(p.t, nameChanged, time.Second, time.Millisecond*5)
	})
}

// Tests for changing SDK key/mobile key are in autoconfig_key_change_test.go, since there are so many consequences

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
