package relay

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// clauseProblem reads the "problem" reported for one clause of a verdict body. It is empty for a
// clause the relay evaluated.
func clauseProblem(body []byte, index int) string {
	return ldvalue.Parse(body).GetByKey("results").GetByIndex(index).GetByKey("problem").StringValue()
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

			t.Run("unparseable clause returns 400", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL("/status", "status"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				assert.NotEmpty(t, clauseProblem(body, 0))
			})

			t.Run("present-but-empty expect value returns 400", func(t *testing.T) {
				r, _ := http.NewRequest("GET", "http://localhost/status?expect=", nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				assert.NotEmpty(t, clauseProblem(body, 0))
			})

			t.Run("unsupported operator returns 422", func(t *testing.T) {
				for _, clause := range []string{"status>=healthy", "status==healthy"} {
					r, _ := http.NewRequest("GET", statusURL("/status", clause), nil)
					result, body := st.DoRequest(r, p.relay)
					assert.Equal(t, http.StatusUnprocessableEntity, result.StatusCode, clause)
					assert.Contains(t, clauseProblem(body, 0), "is not supported", clause)
				}
			})

			t.Run("unknown field returns 422", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL("/status", "connexionStatus.state=VALID"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusUnprocessableEntity, result.StatusCode)
				assert.Contains(t, clauseProblem(body, 0), "unknown field")
			})

			// An unconfigured environment is a well-formed question with the answer "no", so it
			// stays a 412 rather than joining the unknown-field cases above.
			t.Run("unconfigured environment returns 412", func(t *testing.T) {
				r, _ := http.NewRequest("GET",
					statusURL("/status", "environments.no-such-env.status=connected"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusPreconditionFailed, result.StatusCode)
				assert.Empty(t, clauseProblem(body, 0))
			})

			t.Run("every clause is reported and the worst outcome wins", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL("/status",
					"status", "connexionStatus.state=VALID", "status=degraded", "status=healthy"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				results := ldvalue.Parse(body).GetByKey("results")
				require.Equal(t, 4, results.Count())
				assert.NotEmpty(t, clauseProblem(body, 0))
				assert.NotEmpty(t, clauseProblem(body, 1))
				assert.Empty(t, clauseProblem(body, 2))
				assert.False(t, results.GetByIndex(2).GetByKey("ok").BoolValue())
				assert.True(t, results.GetByIndex(3).GetByKey("ok").BoolValue())
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

			t.Run("all-environments path is unknown on this route", func(t *testing.T) {
				r, _ := http.NewRequest("GET",
					statusURL(envPath, "environments.anything.status=connected"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusUnprocessableEntity, result.StatusCode)
				assert.Contains(t, clauseProblem(body, 0), "unknown field")
			})

			t.Run("unsupported operator returns 422", func(t *testing.T) {
				r, _ := http.NewRequest("GET", statusURL(envPath, "status~=connected"), nil)
				result, body := st.DoRequest(r, p.relay)
				assert.Equal(t, http.StatusUnprocessableEntity, result.StatusCode)
				assert.Contains(t, clauseProblem(body, 0), "is not supported")
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
