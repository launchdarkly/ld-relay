package relay

import (
	"encoding/json"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/browser"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/lduser"

	"github.com/stretchr/testify/assert"
)

// The tests for individual JS browser endpoints already verify that the basic CORS headers
// are returned by each. The tests in this file verify optional configurable CORS behavior.
// Since we use the same CORS middleware for all browser endpoints, we will only use a single
// endpoint for these tests.

func TestEndpointsJSClientCORS(t *testing.T) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
	user := lduser.NewUser("me")
	userJSON, _ := json.Marshal(user)
	expectedJSEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, false, false))

	endpoint := endpointTestParams{
		"get eval", "GET", "/sdk/eval/$ENV/users/$USER", userJSON, envID, http.StatusOK, expectedJSEvalBody,
	}

	t.Run("default Access-Control-Allow-Headers", func(t *testing.T) {
		config := c.Config{Environment: st.MakeEnvConfigs(st.EnvClientSide)}
		withStartedRelay(t, config, func(p relayTestParams) {
			result, _ := st.DoRequest(endpoint.request(), p.relay)
			if assert.Equal(t, endpoint.expectedStatus, result.StatusCode) {
				assert.Equal(t, browser.DefaultAllowedHeaders, result.Header.Get("Access-Control-Allow-Headers"))
			}
		})
	})

	t.Run("Access-Control-Allow-Header with custom values", func(t *testing.T) {
		env := st.EnvClientSide
		env.Config.AllowedHeader = configtypes.NewOptStringList([]string{"my-header-1", "my-header-2"})
		config := c.Config{Environment: st.MakeEnvConfigs(env)}
		withStartedRelay(t, config, func(p relayTestParams) {
			result, _ := st.DoRequest(endpoint.request(), p.relay)
			if assert.Equal(t, endpoint.expectedStatus, result.StatusCode) {
				assert.Equal(t, browser.DefaultAllowedHeaders+",my-header-1,my-header-2", result.Header.Get("Access-Control-Allow-Headers"))
			}
		})
	})

	t.Run("default Access-Control-Allow-Origin", func(t *testing.T) {
		config := c.Config{Environment: st.MakeEnvConfigs(st.EnvClientSide)}
		withStartedRelay(t, config, func(p relayTestParams) {
			result, _ := st.DoRequest(endpoint.request(), p.relay)
			if assert.Equal(t, endpoint.expectedStatus, result.StatusCode) {
				assert.Equal(t, browser.DefaultAllowedOrigin, result.Header.Get("Access-Control-Allow-Origin"))
			}
		})
	})

	t.Run("Access-Control-Allow-Origin with custom value that matches the request origin", func(t *testing.T) {
		allowedOrigin1 := "http://non-matching-origin"
		allowedOrigin2 := "http://desired-origin"
		env := st.EnvClientSide
		env.Config.AllowedOrigin = configtypes.NewOptStringList([]string{allowedOrigin1, allowedOrigin2})
		config := c.Config{Environment: st.MakeEnvConfigs(env)}
		withStartedRelay(t, config, func(p relayTestParams) {
			req := endpoint.request()
			req.Header.Set("Origin", allowedOrigin2)
			result, _ := st.DoRequest(req, p.relay)
			if assert.Equal(t, endpoint.expectedStatus, result.StatusCode) {
				assert.Equal(t, allowedOrigin2, result.Header.Get("Access-Control-Allow-Origin"))
			}
		})
	})

	t.Run("Access-Control-Allow-Origin with custom values that do not match the request origin", func(t *testing.T) {
		allowedOrigin1 := "http://non-matching-origin"
		allowedOrigin2 := "http://desired-origin"
		actualOrigin := "http://example"
		env := st.EnvClientSide
		env.Config.AllowedOrigin = configtypes.NewOptStringList([]string{allowedOrigin1, allowedOrigin2})
		config := c.Config{Environment: st.MakeEnvConfigs(env)}
		withStartedRelay(t, config, func(p relayTestParams) {
			req := endpoint.request()
			req.Header.Set("Origin", actualOrigin)
			result, _ := st.DoRequest(req, p.relay)
			if assert.Equal(t, endpoint.expectedStatus, result.StatusCode) {
				// The defined behavior here, when the actual origin didn't match any of the configured
				// allowable ones, is that we return the *first* configured one.
				assert.Equal(t, allowedOrigin1, result.Header.Get("Access-Control-Allow-Origin"))
			}
		})
	})
}
