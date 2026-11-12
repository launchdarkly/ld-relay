//go:build redis_unit_tests
// +build redis_unit_tests

package relay

// Continuation of relay_end_to_end_test.go that includes persistent storage behavior. A Redis server
// must be running on localhost for these tests.

import (
	"io"
	"net/http"
	"testing"
	"time"

	"log/slog"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservicesv2"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	basicRedisConfig    = c.RedisConfig{Host: "localhost", LocalTTL: configtypes.NewOptDuration(time.Minute)}
	uncachedRedisConfig = c.RedisConfig{Host: "localhost", LocalTTL: configtypes.NewOptDuration(0)}
)

func TestRelayEndToEndRedisSuccessWithCache(t *testing.T) {
	initialData := ldservicesv2.NewServerSDKData().Flags(testFlag)
	protocol := ldservicesv2.NewStreamingProtocol().
		WithIntent(subsystems.ServerIntent{Payload: subsystems.Payload{
			ID:     "fake-id",
			Target: 0,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		}}).
		WithPutObjects(initialData.ToPutObjects()).
		WithTransferred("state", 1)
	streamHandler, _ := ldservices.ServerSideStreamingV2ServiceProtocolHandler(protocol)
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
	initialData := ldservicesv2.NewServerSDKData().Flags(testFlag)
	protocol := ldservicesv2.NewStreamingProtocol().
		WithIntent(subsystems.ServerIntent{Payload: subsystems.Payload{
			ID:     "fake-id",
			Target: 0,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		}}).
		WithPutObjects(initialData.ToPutObjects()).
		WithTransferred("state", 1)
	streamHandler, _ := ldservices.ServerSideStreamingV2ServiceProtocolHandler(protocol)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: uncachedRedisConfig}
	relayEndToEndTest(t, config, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
		p.waitForSuccessfulInit()
		p.expectSuccessFromAllEndpoints(testEnv)
	})
}

func TestRelayEndToEndRedisInitTimeoutWithInitializedDataStore(t *testing.T) {
	initialData := ldservicesv2.NewServerSDKData().Flags(testFlag)
	protocol := ldservicesv2.NewStreamingProtocol().
		WithIntent(subsystems.ServerIntent{Payload: subsystems.Payload{
			ID:     "fake-id",
			Target: 0,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		}}).
		WithPutObjects(initialData.ToPutObjects()).
		WithTransferred("state", 1)
	streamHandler, _ := ldservices.ServerSideStreamingV2ServiceProtocolHandler(protocol)
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
		p.waitForLogMessage(slog.LevelError, "error initializing LaunchDarkly client",
			"initialization timeout")
		p.expectSuccessFromAllEndpoints(testEnv)

		// The data in the database has no FDv2 selector, so the FDv2 polling endpoints serve it
		// as a full payload without one.
		for _, req := range []*http.Request{
			st.BuildRequestWithAuth("GET", p.relayURL+"/sdk/poll", testEnv.Config.SDKKey, nil),
			st.BuildRequestWithAuth("GET", p.relayURL+"/sdk/poll/eval/"+st.ToBase64(st.SimpleUserJSON),
				testEnv.Config.MobileKey, nil),
		} {
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, req.URL.Path)
			assert.NotEmpty(t, resp.Header.Get("Etag"), req.URL.Path)
			assert.Contains(t, string(body), `"key":"`+testFlag.Key+`"`, req.URL.Path)
			assert.Contains(t, string(body), `{"state":"","version":0}`, req.URL.Path)
		}
	})
}
