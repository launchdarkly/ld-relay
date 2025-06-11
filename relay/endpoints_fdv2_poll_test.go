package relay

import (
	"encoding/json"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/stretchr/testify/assert"
)

type fdv2PollEndpointTestParams struct {
	endpointTestParams
	expectedEvents    []string
	expectedEventData [][]byte
}

func TestFDv2PollingEndpoint(t *testing.T) {
	sdkKeyMain := st.EnvMain.Config.SDKKey

	specs := []fdv2PollEndpointTestParams{
		{
			endpointTestParams: endpointTestParams{"poll", "GET", "/sdk/poll", nil, sdkKeyMain, http.StatusOK, st.ExpectNoBody()},
			expectedEvents: []string{
				"server-intent",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"payload-transferred",
			},
		},
		{
			endpointTestParams: endpointTestParams{"poll", "GET", "/sdk/poll?basis=initial-state", nil, sdkKeyMain, http.StatusOK, st.ExpectNoBody()},
			expectedEvents: []string{
				"server-intent",
			},
		},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvWithTTL)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			t.Run(spec.name, func(t *testing.T) {
				result, body := st.DoRequest(spec.request(), p.relay)

				var payload pollingPayload
				err := json.Unmarshal(body, &payload)
				assert.NoError(t, err)

				assert.Len(t, payload.Events, len(spec.expectedEvents), "Unexpected number of events")

				assert.Equal(t, spec.expectedStatus, result.StatusCode)
				st.AssertNonStreamingHeaders(t, result.Header)
			})
		}
	})
}
