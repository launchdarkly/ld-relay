package relay

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/eventsource"
	c "github.com/launchdarkly/ld-relay/v6/config"
)

type streamEndpointTestParams struct {
	endpointTestParams
	expectedEvent string
	expectedData  []byte
}

func (s streamEndpointTestParams) assertRequestReceivesEvent(t *testing.T, r *http.Request, relay *Relay) *http.Response {
	return withStreamRequest(t, r, relay, func(eventCh <-chan eventsource.Event) {
		select {
		case event := <-eventCh:
			if event != nil {
				assert.Equal(t, s.expectedEvent, event.Event())
				if s.expectedData != nil {
					assert.JSONEq(t, string(s.expectedData), event.Data())
				}
			}
		case <-time.After(time.Second * 3):
			assert.Fail(t, "timed out waiting for event")
		}
	})
}

func TestRelayServerSideStreams(t *testing.T) {
	env := testEnvMain
	sdkKey := env.config.SDKKey
	expectedFlagsData, _ := json.Marshal(flagsMap(allFlags))
	expectedAllData, _ := json.Marshal(map[string]map[string]interface{}{
		"data": {
			"flags": flagsMap(allFlags),
			"segments": map[string]interface{}{
				segment1.Key: &segment1,
			},
		},
	})

	specs := []streamEndpointTestParams{
		{endpointTestParams{"flags stream", "GET", "/flags", nil, sdkKey, 200, nil}, "put", expectedFlagsData},
		{endpointTestParams{"all stream", "GET", "/all", nil, sdkKey, 200, nil}, "put", expectedAllData},
	}

	config := c.DefaultConfig
	config.Environment = makeEnvConfigs(env)

	relayTest(config, func(p relayTestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					s.assertRequestReceivesEvent(t, s.request(), p.relay)
				})

				t.Run("unknown SDK key", func(t *testing.T) {
					s1 := s
					s1.credential = undefinedSDKKey
					result, _ := doRequest(s1.request(), p.relay)

					assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
					// t.Fail()
				})
			})
		}
	})
}

func TestRelayMobileStreams(t *testing.T) {
	env := testEnvMobile
	userJSON := []byte(`{"key":"me"}`)

	specs := []streamEndpointTestParams{
		{endpointTestParams{"mobile ping", "GET", "/mping", nil, env.config.MobileKey, 200, nil},
			"ping", nil},
		{endpointTestParams{"mobile stream GET", "GET", "/meval/$DATA", userJSON, env.config.MobileKey, 200, nil},
			"ping", nil},
		{endpointTestParams{"mobile stream REPORT", "REPORT", "/meval", userJSON, env.config.MobileKey, 200, nil},
			"ping", nil},
	}

	config := c.DefaultConfig
	config.Environment = makeEnvConfigs(env)

	relayTest(config, func(p relayTestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					s.assertRequestReceivesEvent(t, s.request(), p.relay)
				})

				t.Run("unknown mobile key", func(t *testing.T) {
					s1 := s
					s1.credential = undefinedMobileKey
					result, _ := doRequest(s1.request(), p.relay)

					assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
				})
			})
		}
	})
}

func TestRelayJSClientStreams(t *testing.T) {
	env := testEnvClientSide
	envID := env.config.EnvID
	userJSON := []byte(`{"key":"me"}`)

	specs := []streamEndpointTestParams{
		{endpointTestParams{"client-side get ping", "GET", "/ping/$ENV", nil, envID, 200, nil},
			"ping", nil},
		{endpointTestParams{"client-side get eval stream", "GET", "/eval/$ENV/$DATA", userJSON, envID, 200, nil},
			"ping", nil},
		{endpointTestParams{"client-side report eval stream", "REPORT", "/eval/$ENV", userJSON, envID, 200, nil},
			"ping", nil},
	}

	config := c.DefaultConfig
	config.Environment = makeEnvConfigs(env)

	relayTest(config, func(p relayTestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("requests", func(t *testing.T) {
					result := s.assertRequestReceivesEvent(t, s.request(), p.relay)

					assertStreamingHeaders(t, result.Header)
					assertExpectedCORSHeaders(t, result, s.method, "*")
				})

				t.Run("options", func(t *testing.T) {
					assertEndpointSupportsOptionsRequest(t, p.relay, s.localURL(), s.method)
				})
			})
		}
	})
}
