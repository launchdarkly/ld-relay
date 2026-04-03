package relay

import (
	"fmt"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"

	"github.com/stretchr/testify/assert"
)

func TestEndpointsSingleEnvironmentStatus(t *testing.T) {
	t.Run("status by environment ID", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvClientSide, st.EnvMobile)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Request status for a specific environment by its environment ID
			url := fmt.Sprintf("http://localhost/status/%s", st.EnvClientSide.Config.EnvID)
			r, _ := http.NewRequest("GET", url, nil)
			result, body := st.DoRequest(r, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			// Verify it returns a single environment object (not wrapped in map)
			// The response should have environment fields directly, not under "environments" key
			assert.Equal(t, ldvalue.NullType, status.GetByKey("environments").Type(),
				"Response should not have 'environments' key - it should be a single environment object")

			// Verify environment-specific fields
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(st.EnvClientSide.Config.SDKKey)),
				status, "sdkKey")
			st.AssertJSONPathMatch(t, string(st.EnvClientSide.Config.EnvID),
				status, "envId")
			st.AssertJSONPathMatch(t, "connected", status, "status")
			st.AssertJSONPathMatch(t, "VALID", status, "connectionStatus", "state")
		})
	})

	t.Run("status by configured name", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvMobile)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Request status for a specific environment by its configured name
			url := fmt.Sprintf("http://localhost/status/%s", st.EnvMain.Name)
			r, _ := http.NewRequest("GET", url, nil)
			result, body := st.DoRequest(r, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			// Verify it returns a single environment object
			assert.Equal(t, ldvalue.NullType, status.GetByKey("environments").Type())

			// Verify environment-specific fields
			st.AssertJSONPathMatch(t, sdks.ObscureKey(string(st.EnvMain.Config.SDKKey)),
				status, "sdkKey")
			st.AssertJSONPathMatch(t, "connected", status, "status")
		})
	})

	t.Run("status with URL-encoded name", func(t *testing.T) {
		var config c.Config
		envWithSpaces := st.EnvMain
		envWithSpaces.Name = "My Production Env"
		config.Environment = st.MakeEnvConfigs(envWithSpaces)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Request with URL-encoded name (spaces as %20)
			url := "http://localhost/status/My%20Production%20Env"
			r, _ := http.NewRequest("GET", url, nil)
			result, body := st.DoRequest(r, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			// Verify it returns a single environment object
			assert.Equal(t, ldvalue.NullType, status.GetByKey("environments").Type())
			st.AssertJSONPathMatch(t, "connected", status, "status")
		})
	})

	t.Run("404 for non-existent environment", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Request status for non-existent environment
			url := "http://localhost/status/nonexistent-env-id"
			r, _ := http.NewRequest("GET", url, nil)
			result, body := st.DoRequest(r, p.relay)

			assert.Equal(t, http.StatusNotFound, result.StatusCode)

			// Verify error response format
			errorResp := ldvalue.Parse(body)
			assert.NotEqual(t, "", errorResp.GetByKey("error").StringValue())
		})
	})

	t.Run("404 for non-existent filter", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvClientSide)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Request status for environment with non-existent filter
			url := fmt.Sprintf("http://localhost/status/%s/filters/nonexistent-filter", st.EnvClientSide.Config.EnvID)
			r, _ := http.NewRequest("GET", url, nil)
			result, body := st.DoRequest(r, p.relay)

			assert.Equal(t, http.StatusNotFound, result.StatusCode)

			// Verify error response
			errorResp := ldvalue.Parse(body)
			assert.NotEqual(t, "", errorResp.GetByKey("error").StringValue())
		})
	})

	t.Run("response has expected structure", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvClientSide)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Get status from single environment endpoint
			url := fmt.Sprintf("http://localhost/status/%s", st.EnvClientSide.Config.EnvID)
			r, _ := http.NewRequest("GET", url, nil)
			result, body := st.DoRequest(r, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)
			status := ldvalue.Parse(body)

			// Verify response has expected structure (not wrapped in "environments" key)
			assert.Equal(t, ldvalue.NullType, status.GetByKey("environments").Type(),
				"Single environment response should not have 'environments' wrapper")

			// Verify all expected fields are present
			st.AssertJSONPathMatch(t, string(st.EnvClientSide.Config.EnvID), status, "envId")
			assert.NotEqual(t, "", status.GetByKey("sdkKey").StringValue(), "Should have sdkKey")
			assert.NotEqual(t, "", status.GetByKey("status").StringValue(), "Should have status")
			assert.NotEqual(t, ldvalue.NullType, status.GetByKey("connectionStatus").Type(), "Should have connectionStatus")
			assert.NotEqual(t, ldvalue.NullType, status.GetByKey("dataStoreStatus").Type(), "Should have dataStoreStatus")
		})
	})

	t.Run("multiple environments - can query each individually", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvClientSide, st.EnvMobile)

		withStartedRelay(t, config, func(p relayTestParams) {
			// Query each environment individually
			envs := []struct {
				name string
				id   string
			}{
				{st.EnvMain.Name, ""},
				{st.EnvClientSide.Name, string(st.EnvClientSide.Config.EnvID)},
				{st.EnvMobile.Name, string(st.EnvMobile.Config.EnvID)},
			}

			for _, env := range envs {
				identifier := env.name
				if env.id != "" {
					identifier = env.id
				}

				url := fmt.Sprintf("http://localhost/status/%s", identifier)
				r, _ := http.NewRequest("GET", url, nil)
				result, body := st.DoRequest(r, p.relay)

				assert.Equal(t, http.StatusOK, result.StatusCode,
					"Expected 200 for environment %s", env.name)

				status := ldvalue.Parse(body)
				st.AssertJSONPathMatch(t, "connected", status, "status")
			}
		})
	})
}
