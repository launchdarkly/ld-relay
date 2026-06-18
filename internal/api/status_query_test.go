package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusRepBody marshals a representative full-status (/status) body.
func statusRepBody(t *testing.T) []byte {
	t.Helper()
	rep := StatusRep{
		Status:        "healthy",
		Version:       "9.0.0",
		ClientVersion: "7.15.2",
		Environments: map[string]EnvironmentStatusRep{
			"My Project my-env": {
				SDKKey:  "sdk-***",
				EnvID:   "env-123",
				EnvKey:  "my-env",
				ProjKey: "my-proj",
				Status:  "connected",
				ConnectionStatus: ConnectionStatusRep{
					State:      "VALID",
					StateSince: 1600000000000,
				},
				DataStoreStatus: DataStoreStatusRep{
					State: "VALID",
				},
				BigSegmentStatus: &BigSegmentStatusRep{
					Available:        true,
					PotentiallyStale: false,
				},
			},
		},
	}
	data, err := json.Marshal(rep)
	require.NoError(t, err)
	return data
}

// envRepBody marshals a representative single-environment body, as returned by the per-env routes.
func envRepBody(t *testing.T) []byte {
	t.Helper()
	rep := EnvironmentStatusRep{
		SDKKey: "sdk-***",
		EnvID:  "env-123",
		Status: "connected",
		ConnectionStatus: ConnectionStatusRep{
			State:      "VALID",
			StateSince: 1600000000000,
		},
		DataStoreStatus: DataStoreStatusRep{State: "VALID"},
	}
	data, err := json.Marshal(rep)
	require.NoError(t, err)
	return data
}

func TestEvaluateExpectationsSatisfied(t *testing.T) {
	body := statusRepBody(t)

	t.Run("single equals clause holds", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"status=healthy"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
		require.Len(t, res.Results, 1)
		assert.True(t, res.Results[0].OK)
		assert.Equal(t, "healthy", res.Results[0].Actual)
	})

	t.Run("nested path holds", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.My Project my-env.connectionStatus.state=VALID"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("not-equals clause holds", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"status!=degraded"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("all clauses AND-ed", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{
			"status=healthy",
			"environments.My Project my-env.status=connected",
		})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
		assert.Len(t, res.Results, 2)
	})

	t.Run("boolean scalar", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.My Project my-env.bigSegmentStatus.available=true"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("numeric scalar rendered without exponent", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.My Project my-env.connectionStatus.stateSince=1600000000000"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})
}

func TestEvaluateExpectationsUnsatisfied(t *testing.T) {
	body := statusRepBody(t)

	t.Run("mismatch yields 412", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"status=degraded"})
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Satisfied)
		require.Len(t, res.Results, 1)
		assert.False(t, res.Results[0].OK)
		assert.Equal(t, "degraded", res.Results[0].Expected)
		assert.Equal(t, "healthy", res.Results[0].Actual)
	})

	t.Run("one failing clause fails the whole request", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{
			"status=healthy",
			"environments.My Project my-env.status=disconnected",
		})
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Satisfied)
		assert.True(t, res.Results[0].OK)
		assert.False(t, res.Results[1].OK)
	})

	t.Run("missing field is unsatisfied even for not-equals", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"environments.no-such-env.status!=connected"})
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Satisfied)
		assert.False(t, res.Results[0].OK)
		assert.Empty(t, res.Results[0].Actual)
	})

	t.Run("path landing on a non-scalar is unsatisfied", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"environments=anything"})
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Results[0].OK)
	})
}

func TestEvaluateExpectationsMalformed(t *testing.T) {
	body := statusRepBody(t)

	for _, clause := range []string{
		"status",            // no operator
		"=healthy",          // no path
		"environments[.x=1", // unterminated bracket
		"a[]=1",             // empty bracket
		`a["]=1`,            // lone double-quote in bracket (must not panic)
		"a[']=1",            // lone single-quote in bracket (must not panic)
		`a["]`,              // lone double-quote, no operator after
	} {
		t.Run(clause, func(t *testing.T) {
			res, code := EvaluateExpectations(body, []string{clause})
			assert.Equal(t, http.StatusBadRequest, code)
			assert.False(t, res.Satisfied)
			assert.NotEmpty(t, res.Error)
		})
	}
}

func TestEvaluateExpectationsStrayBracket(t *testing.T) {
	body := statusRepBody(t)

	// An unbalanced ']' must not drive bracket depth negative and hide the top-level operator.
	// The operator is still found, the resulting path simply does not resolve -> unsatisfied (412),
	// not a 400 for the wrong reason and not a crash.
	res, code := EvaluateExpectations(body, []string{"a]b=c"})
	assert.Equal(t, http.StatusPreconditionFailed, code)
	assert.False(t, res.Satisfied)
	require.Len(t, res.Results, 1)
	assert.False(t, res.Results[0].OK)
}

func TestEvaluateExpectationsExplicitNull(t *testing.T) {
	// A field whose value is explicit JSON null renders as the string "null".
	body, err := json.Marshal(map[string]interface{}{"thing": nil})
	require.NoError(t, err)

	res, code := EvaluateExpectations(body, []string{"thing=null"})
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, res.Satisfied)
}

func TestEvaluateExpectationsSingleEnvBody(t *testing.T) {
	body := envRepBody(t)

	// Against a single-environment body, paths are relative to the environment object itself --
	// no "environments" wrapper.
	res, code := EvaluateExpectations(body, []string{
		"status=connected",
		"connectionStatus.state=VALID",
	})
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, res.Satisfied)
}

func TestEvaluateExpectationsBracketQuotedKey(t *testing.T) {
	// A key containing a dot must be addressable via bracket-quoting.
	rep := map[string]interface{}{
		"environments": map[string]interface{}{
			"proj.env": map[string]interface{}{"status": "connected"},
		},
	}
	body, err := json.Marshal(rep)
	require.NoError(t, err)

	res, code := EvaluateExpectations(body, []string{`environments["proj.env"].status=connected`})
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, res.Satisfied)
}

func TestEvaluateExpectationsArrayAccess(t *testing.T) {
	// Forward-compat: addressing concurrent-keys-style arrays by index and by field filter.
	rep := map[string]interface{}{
		"sdkKeys": []interface{}{
			map[string]interface{}{"key": "old-default", "value": "sdk-aaa"},
			map[string]interface{}{"key": "new-production-default", "value": "sdk-bbb"},
		},
	}
	body, err := json.Marshal(rep)
	require.NoError(t, err)

	t.Run("by index", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"sdkKeys[0].key=old-default"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("by field filter", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"sdkKeys[key=new-production-default].value=sdk-bbb"})
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("filter with no match is unsatisfied", func(t *testing.T) {
		_, code := EvaluateExpectations(body, []string{"sdkKeys[key=missing].value=x"})
		assert.Equal(t, http.StatusPreconditionFailed, code)
	})

	t.Run("index out of range is unsatisfied", func(t *testing.T) {
		_, code := EvaluateExpectations(body, []string{"sdkKeys[5].key=x"})
		assert.Equal(t, http.StatusPreconditionFailed, code)
	})
}
