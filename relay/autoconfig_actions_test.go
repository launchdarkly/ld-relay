package relay

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
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
	autoConfTestWithClientFactory(t, config, initialEvent,
		func(createdCh chan<- *testclient.FakeLDClient) sdks.ClientFactoryFunc {
			return testclient.FakeLDClientFactoryWithChannel(true, createdCh)
		}, action)
}

// autoConfTestWithClientFactory is autoConfTest with a caller-supplied SDK client factory, so a test
// can inject a factory that fails or hangs for specific keys (e.g. to exercise the re-anchor
// init-failure rollback through the real RAC handler). makeClientFactory receives the channel that
// created clients are reported on; the usual body wraps testclient.FakeLDClientFactoryWithChannel and
// special-cases only the keys it wants to treat differently, forwarding the rest to the healthy factory.
func autoConfTestWithClientFactory(
	t *testing.T,
	config c.Config,
	initialEvent *httphelpers.SSEEvent,
	makeClientFactory func(createdCh chan<- *testclient.FakeLDClient) sdks.ClientFactoryFunc,
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

			// In tests involving adding/removing credentials, allow Relay to clean up credentials quickly so as not
			// to take more time than necessary to verify the test conditions.
			config.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(time.Millisecond * 100)

			relay, err := newRelayInternal(config, relayInternalOptions{
				loggers:       mockLog.Loggers,
				clientFactory: makeClientFactory(clientsCreatedCh),
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
		assert.Equal(t, testAutoConfEnv1.SDKKey(), client1.Key)
		assert.Equal(t, testAutoConfEnv2.SDKKey(), client2.Key)

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
		// Only the anchor (newKey) opens an upstream client; the expiring oldKey is accepted
		// locally but shares the anchor's connection (anchor-only upstream client).
		anchorClient := p.awaitClient()
		assert.Equal(t, newKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

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
		assert.Equal(t, testAutoConfEnv1.SDKKey(), client1.Key)

		env1 := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env1)

		p.stream.Enqueue(makeAutoConfPatchEvent(testAutoConfEnv2))

		client2 := p.awaitClient()
		assert.Equal(t, testAutoConfEnv2.SDKKey(), client2.Key)

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

		// Only the anchor (newKey) opens an upstream client; the expiring oldKey is accepted
		// locally but shares the anchor's connection (anchor-only upstream client).
		anchorClient := p.awaitClient()
		assert.Equal(t, newKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		env := p.awaitEnvironment(envWithKeys.id)
		assertEnvProps(t, envWithKeys.params(), env)

		// Both the new anchor key and the expiring old key are in the accepted set until oldKey expires.
		expectedCredentials := credentialsAsSet(envWithKeys.id, envWithKeys.mobKey, newKey, oldKey)
		assert.Equal(t, expectedCredentials, credentialsAsSet(env.GetCredentials()...))

		paramsWithOldKey := envWithKeys.params()
		paramsWithOldKey.SDKKey = oldKey
		p.assertEnvLookup(env, paramsWithOldKey)
	})
}

// When addEnvironment fails, the auto-config handler must not go on to call ReconcileCredentials on
// the nil EnvContext it got back. This is only reachable when the payload also carries an expiring
// SDK key (the gate that triggers the credential update). We force the failure deterministically by
// closing the Relay first, so addEnvironment returns errAlreadyClosed with a nil env.
func TestAutoConfigAddEnvironmentWithExpiringSDKKeyDoesNotPanicWhenInitFails(t *testing.T) {
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
		params := envWithKeys.params()
		require.True(t, params.ExpiringSDKKey.Defined(),
			"precondition: params must carry an expiring SDK key to reach the credential-update branch")

		// Closing the Relay makes the next addEnvironment return (nil, nil, errAlreadyClosed).
		require.NoError(t, p.relay.Close())

		actions := &relayAutoConfigActions{r: p.relay}
		require.NotPanics(t, func() {
			actions.AddEnvironment(params)
		}, "AddEnvironment must not dereference a nil EnvContext when addEnvironment fails")
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
