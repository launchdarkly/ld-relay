package relay

import (
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file verify the auto-configuration behavior of Relay, assuming that the
// low-level StreamManager implementation is working correctly. StreamManager is tested more thoroughly,
// including error conditions and reconnection, in the autoconfig package where it is implemented.
//
// These tests use a real HTTP server to provide the configuration stream, but they use FakeLDClient
// instead of creating real SDK clients, so there are no SDK connections made.

type autoConfTestParams struct {
	relayTestHelper
	t                *testing.T
	relay            *Relay
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
	defer mockLog.DumpIfTestFailed(t)

	streamHandler, stream := httphelpers.SSEHandler(initialEvent)
	defer stream.Close()
	streamRequestsHandler, streamRequestsCh := httphelpers.RecordingHandler(streamHandler)

	eventRequestsHandler, eventRequestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))

	clientsCreatedCh := make(chan *testclient.FakeLDClient, 10)

	p := autoConfTestParams{
		relayTestHelper:  relayTestHelper{t: t},
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

			relay, err := newRelayInternal(config, relayInternalOptions{
				loggers:       mockLog.Loggers,
				clientFactory: testclient.FakeLDClientFactoryWithChannel(true, clientsCreatedCh),
			})
			if err != nil {
				panic(err)
			}

			p.relay = relay
			p.relayTestHelper.relay = relay
			defer relay.Close()
			action(p)
		})
	})
}

func (p autoConfTestParams) awaitClient() *testclient.FakeLDClient {
	return helpers.RequireValue(p.t, p.clientsCreatedCh, 1000*time.Second, "timed out waiting for client creation")
}

func (p autoConfTestParams) shouldNotCreateClient(timeout time.Duration) {
	if !helpers.AssertNoMoreValues(p.t, p.clientsCreatedCh, timeout, "unexpectedly created client") {
		p.t.FailNow()
	}
}

func TestAutoConfigInit(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1, testAutoConfEnv2)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		client2 := p.awaitClient()
		if client1.Key == testAutoConfEnv2.SDKKey() {
			client1, client2 = client2, client1
		}
		assert.Equal(t, testAutoConfEnv1.sdkKey, client1.Key)
		assert.Equal(t, testAutoConfEnv2.sdkKey, client2.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env1)
		p.assertEnvLookup(env1, testAutoConfEnv1.params())

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2.params(), env2)
		p.assertEnvLookup(env2, testAutoConfEnv2.params())
	})
}

func TestAutoConfigInitWithExpiringSDKKey(t *testing.T) {
	newKey := c.SDKKey("newsdkkey")
	oldKey := c.SDKKey("oldsdkkey")
	envWithKeys := testAutoConfEnv1
	envWithKeys.sdkKey = envfactory.SDKKeyRep{
		Value: newKey,
		Expiring: envfactory.ExpiringKeyRep{
			Value:     oldKey,
			Timestamp: ldtime.UnixMillisNow() + 100000,
		},
	}
	initialEvent := makeAutoConfPutEvent(envWithKeys)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		client2 := p.awaitClient()
		if client1.Key == oldKey {
			client1, client2 = client2, client1
		}
		assert.Equal(t, newKey, client1.Key)
		assert.Equal(t, oldKey, client2.Key)

		env := p.awaitEnvironment(envWithKeys.id)
		assertEnvProps(t, envWithKeys.params(), env)
		p.assertEnvLookup(env, envWithKeys.params())

		paramsWithOldKey := envWithKeys.params()
		paramsWithOldKey.SDKKey = oldKey
		p.assertEnvLookup(env, paramsWithOldKey)
	})
}

func TestAutoConfigInitAfterPreviousInitCanAddAndRemoveEnvs(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv1.SDKKey(), client1.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env1)
		p.assertEnvLookup(env1, testAutoConfEnv1.params())

		p.stream.Enqueue(makeAutoConfPutEvent(testAutoConfEnv2))

		client2 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv2.SDKKey(), client2.Key)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2.params(), env2)
		p.assertEnvLookup(env2, testAutoConfEnv2.params())

		client1.AwaitClose(t, time.Second)

		p.shouldNotHaveEnvironment(testAutoConfEnv1.id, time.Millisecond*100)
		p.assertSDKEndpointsAvailability(
			false,
			testAutoConfEnv1.SDKKey(),
			testAutoConfEnv1.mobKey,
			testAutoConfEnv1.id,
		)
	})
}

func TestAutoConfigAddEnvironment(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv1.sdkKey, client1.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env1)

		p.stream.Enqueue(makeAutoConfPatchEvent(testAutoConfEnv2))

		client2 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv2.sdkKey, client2.Key)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		p.assertEnvLookup(env2, testAutoConfEnv2.params())
		assertEnvProps(t, testAutoConfEnv2.params(), env2)
	})
}

func TestAutoConfigAddEnvironmentWithExpiringSDKKey(t *testing.T) {
	newKey := c.SDKKey("newsdkkey")
	oldKey := c.SDKKey("oldsdkkey")
	envWithKeys := testAutoConfEnv1
	envWithKeys.sdkKey = envfactory.SDKKeyRep{
		Value: newKey,
		Expiring: envfactory.ExpiringKeyRep{
			Value:     oldKey,
			Timestamp: ldtime.UnixMillisNow() + 100000,
		},
	}
	initialEvent := makeAutoConfPutEvent()
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		p.stream.Enqueue(makeAutoConfPatchEvent(envWithKeys))

		client1 := p.awaitClient()
		client2 := p.awaitClient()
		if client1.Key == oldKey {
			client1, client2 = client2, client1
		}
		assert.Equal(t, newKey, client1.Key)
		assert.Equal(t, oldKey, client2.Key)

		env := p.awaitEnvironment(envWithKeys.id)
		assertEnvProps(t, envWithKeys.params(), env)

		expectedCredentials := credentialsAsSet(envWithKeys.id, envWithKeys.mobKey, envWithKeys.SDKKey())
		assert.Equal(t, expectedCredentials, credentialsAsSet(env.GetCredentials()...))

		paramsWithOldKey := envWithKeys.params()
		paramsWithOldKey.SDKKey = oldKey
		p.assertEnvLookup(env, paramsWithOldKey)

		if !helpers.AssertChannelNotClosed(t, client2.CloseCh, time.Millisecond*300, "should not have closed client for deprecated key yet") {
			t.FailNow()
		}
	})
}

func TestAutoConfigUpdateEnvironmentName(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

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
		if client1.Key == testAutoConfEnv2.SDKKey() {
			client1, client2 = client2, client1
		}

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env1)

		env2 := p.awaitEnvironment(testAutoConfEnv2.id)
		assertEnvProps(t, testAutoConfEnv2.params(), env2)

		p.stream.Enqueue(makeAutoConfDeleteEvent(testAutoConfEnv1.id, testAutoConfEnv1.version+1))

		client1.AwaitClose(t, time.Second)

		p.shouldNotHaveEnvironment(testAutoConfEnv1.id, time.Millisecond*100)
		p.assertSDKEndpointsAvailability(
			false,
			testAutoConfEnv1.SDKKey(),
			testAutoConfEnv1.mobKey,
			testAutoConfEnv1.id,
		)
	})
}
