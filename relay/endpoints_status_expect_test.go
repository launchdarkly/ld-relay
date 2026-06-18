package relay

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v4/ldvalue"

	"github.com/stretchr/testify/assert"
)

// statusURL builds a status URL with the given path and a set of repeated "expect" clauses,
// properly query-encoded.
func statusURL(path string, clauses ...string) string {
	q := url.Values{}
	for _, clause := range clauses {
		q.Add("expect", clause)
	}
	return fmt.Sprintf("http://localhost%s?%s", path, q.Encode())
}

func TestEndpointsStatusExpect(t *testing.T) {
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvMobile)

	t.Run("all-environments status", func(t *testing.T) {
		withStartedRelay(t, config, func(p relayTestParams) {
			t.Run("satisfied clause returns 200", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL("/status", "status=healthy"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusOK, result.StatusCode)
				assert.True(t, ldvalue.Parse(body).GetByKey("satisfied").BoolValue())
			})

			t.Run("unsatisfied clause returns 412", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL("/status", "status=degraded"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusPreconditionFailed, result.StatusCode)
				assert.False(t, ldvalue.Parse(body).GetByKey("satisfied").BoolValue())
			})

			t.Run("multiple clauses are AND-ed", func(t *testing.T) {
				path := fmt.Sprintf("environments.%s.status", st.EnvMain.Name)
				r, _ := http.NewRequest("GET",
					statusURL("/status", "status=healthy", path+"=connected"), nil)
				result, _ := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusOK, result.StatusCode)

				r, _ = http.NewRequest("GET",
					statusURL("/status", "status=healthy", path+"=disconnected"), nil)
				result, _ = st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusPreconditionFailed, result.StatusCode)
			})

			t.Run("malformed clause returns 400", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL("/status", "status"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				assert.NotEmpty(t, ldvalue.Parse(body).GetByKey("error").StringValue())
			})

			t.Run("present-but-empty expect value returns 400", func(t *testing.T) {
				r, _ := http.NewRequest("GET", "http://localhost/status?expect=", nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				assert.NotEmpty(t, ldvalue.Parse(body).GetByKey("error").StringValue())
			})

			t.Run("no expect param leaves the body unchanged", func(t *testing.T) {
				r, _ := http.NewRequest("GET", "http://localhost/status", nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusOK, result.StatusCode)
				// Full body, not a verdict object.
				assert.Equal(t, ldvalue.NullType,
					ldvalue.Parse(body).GetByKey("satisfied").Type())
				assert.Equal(t, "healthy", ldvalue.Parse(body).GetByKey("status").StringValue())
			})
		})
	})

	t.Run("single-environment status", func(t *testing.T) {
		withStartedRelay(t, config, func(p relayTestParams) {
			envPath := "/status/" + url.PathEscape(st.EnvMain.Name)

			t.Run("paths are relative to the environment object", func(t *testing.T) {
				r, _ := http.NewRequest("GET",
					statusURL(envPath, "status=connected", "connectionStatus.state=VALID"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusOK, result.StatusCode)
				assert.True(t, ldvalue.Parse(body).GetByKey("satisfied").BoolValue())
			})

			t.Run("unsatisfied clause returns 412", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL(envPath, "status=disconnected"), nil)
				result, _ := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusPreconditionFailed, result.StatusCode)
			})

			t.Run("not-equals operator through the handler", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL(envPath, "status!=disconnected"), nil)
				result, _ := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusOK, result.StatusCode)

				r, _ = http.NewRequest("GET", statusURL(envPath, "status!=connected"), nil)
				result, _ = st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusPreconditionFailed, result.StatusCode)
			})

			t.Run("unknown environment still returns 404 before evaluation", func(t *testing.T) {
				r, _ := http.NewRequest("GET",
					statusURL("/status/no-such-env", "status=connected"), nil)
				result, _ := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusNotFound, result.StatusCode)
			})
		})
	})
}
