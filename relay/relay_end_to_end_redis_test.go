//go:build redis_unit_tests
// +build redis_unit_tests

package relay

// Continuation of relay_end_to_end_test.go that includes persistent storage behavior. A Redis server
// must be running on localhost for these tests.

import (
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/testhelpers/ldservices"
	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-configtypes"
)

var basicRedisConfig = c.RedisConfig{Host: "localhost", LocalTTL: configtypes.NewOptDuration(time.Minute)}
var uncachedRedisConfig = c.RedisConfig{Host: "localhost", LocalTTL: configtypes.NewOptDuration(0)}

func TestRelayEndToEndRedisSuccessWithCache(t *testing.T) {
	putEvent := ldservices.NewServerSDKData().Flags(&testFlag).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: basicRedisConfig}
	relayEndToEndTest(t, config, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
		p.waitForSuccessfulInit()
		p.expectSuccessFromAllEndpoints(testEnv)
	})
}

func TestRelayEndToEndRedisSuccessWithoutCache(t *testing.T) {
	// Turning off the cache isn't something that would be done in normal usage, but it lets us verify
	// that Relay will read flags from the database as needed when servicing requests.
	putEvent := ldservices.NewServerSDKData().Flags(&testFlag).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: uncachedRedisConfig}
	relayEndToEndTest(t, config, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
		p.waitForSuccessfulInit()
		p.expectSuccessFromAllEndpoints(testEnv)
	})
}

func TestRelayEndToEndRedisInitTimeoutWithInitializedDataStore(t *testing.T) {
	putEvent := ldservices.NewServerSDKData().Flags(&testFlag).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	hangingHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done() // hang until the request is cancelled by the client or the server
	})
	testEnv := st.EnvWithAllCredentials

	// First, run Relay with a successful connection to fake-LD, just to populate the database.
	preliminaryConfig := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: basicRedisConfig}
	relayEndToEndTest(t, preliminaryConfig, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
		p.waitForSuccessfulInit()
		p.expectSuccessFromAllEndpoints(testEnv)
	})

	// Now, run Relay again against a fake-LD endpoint that hangs without returning any data, and a
	// short initTimeout. Clients should receive the data that's in the database from the previous run.
	config := c.Config{
		Main: c.MainConfig{
			InitTimeout: configtypes.NewOptDuration(time.Millisecond),
		},
		Environment: st.MakeEnvConfigs(testEnv),
		Redis:       basicRedisConfig,
	}
	behavior := relayTestBehavior{skipWaitForEnvironments: true}
	relayEndToEndTest(t, config, behavior, hangingHandler, func(p relayEndToEndTestParams) {
		p.waitForLogMessage(ldlog.Error, "timeout encountered waiting for LaunchDarkly client initialization",
			"initialization timeout")
		p.expectSuccessFromAllEndpoints(testEnv)
	})
}
