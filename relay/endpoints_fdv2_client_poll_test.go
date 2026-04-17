package relay

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFDv2ClientSidePollingWithMobileKey(t *testing.T) {
	env := st.EnvWithAllCredentials
	mobileKey := env.Config.MobileKey
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("GET with context in URL", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))

			// Should have server-intent + put-objects for mobile-visible flags + payload-transferred
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
			assert.Equal(t, "payload-transferred", payload.Events[len(payload.Events)-1].Event)

			// Verify put-objects use flag-eval kind
			for _, event := range payload.Events {
				if event.Event == "put-object" {
					var putObj subsystems.PutObject
					data, err := json.Marshal(event.EventData)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(data, &putObj))
					assert.Equal(t, subsystems.ObjectKind("flag-eval"), putObj.Kind)
				}
			}
		})

		t.Run("POST with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(mobileKey))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("POST", "/sdk/poll/eval", contextJSON, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
		})

		t.Run("REPORT with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(mobileKey))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("REPORT", "/sdk/poll/eval", contextJSON, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
		})

		t.Run("auth via query param", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64+"?auth="+string(mobileKey), nil, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("invalid credential returns 401", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, st.UndefinedMobileKey, nil)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})

		t.Run("no credential returns 401", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, nil)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})

		t.Run("basis param returns up-to-date when matching", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64+"?basis=initial-state", mobileKey, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.Len(t, payload.Events, 1)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
		})
	})
}

func TestFDv2ClientSidePollingWithEnvironmentID(t *testing.T) {
	env := st.EnvWithAllCredentials
	envID := env.Config.EnvID
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("GET with context in URL via header", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)

			// Verify put-objects use flag-eval kind
			for _, event := range payload.Events {
				if event.Event == "put-object" {
					var putObj subsystems.PutObject
					data, err := json.Marshal(event.EventData)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(data, &putObj))
					assert.Equal(t, subsystems.ObjectKind("flag-eval"), putObj.Kind)
				}
			}
		})

		t.Run("POST with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("POST", "/sdk/poll/eval", contextJSON, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("REPORT with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("REPORT", "/sdk/poll/eval", contextJSON, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("auth via query param", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64+"?auth="+string(envID), nil, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("invalid credential returns 401", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(st.UndefinedEnvID))
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, h)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})
	})
}

func TestFDv2ClientSidePollingFiltersFlagsByCredentialType(t *testing.T) {
	env := st.EnvWithAllCredentials
	mobileKey := env.Config.MobileKey
	envID := env.Config.EnvID
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("mobile key sees mobile-visible flags", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			_, body := st.DoRequest(req, p.relay)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))

			flagKeys := collectFlagKeys(t, payload)
			// MobileFlags includes: Flag1ServerSide, Flag2ServerSide, Flag4ClientSide, Flag5ClientSide, Flag7Mobile, Flag8ContextAware
			// Flag3ServerSideNotMobile and Flag6ClientSideNotMobile should be excluded
			assert.NotContains(t, flagKeys, st.Flag3ServerSideNotMobile.Flag.Key)
			assert.NotContains(t, flagKeys, st.Flag6ClientSideNotMobile.Flag.Key)
			assert.Contains(t, flagKeys, st.Flag7Mobile.Flag.Key)
		})

		t.Run("environment ID sees client-side-visible flags", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, h)
			_, body := st.DoRequest(req, p.relay)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))

			flagKeys := collectFlagKeys(t, payload)
			// ClientSideFlags includes: Flag4ClientSide, Flag5ClientSide, Flag6ClientSideNotMobile, Flag8ContextAware
			// Server-side only and mobile-only flags should be excluded
			assert.NotContains(t, flagKeys, st.Flag1ServerSide.Flag.Key)
			assert.NotContains(t, flagKeys, st.Flag2ServerSide.Flag.Key)
			assert.NotContains(t, flagKeys, st.Flag7Mobile.Flag.Key)
			assert.Contains(t, flagKeys, st.Flag4ClientSide.Flag.Key)
			assert.Contains(t, flagKeys, st.Flag6ClientSideNotMobile.Flag.Key)
		})
	})
}

func collectFlagKeys(t *testing.T, payload pollingPayload) []string {
	t.Helper()
	var keys []string
	for _, event := range payload.Events {
		if event.Event == "put-object" {
			data, err := json.Marshal(event.EventData)
			require.NoError(t, err)
			var putObj subsystems.PutObject
			require.NoError(t, json.Unmarshal(data, &putObj))
			keys = append(keys, putObj.Key)
		}
	}
	return keys
}
