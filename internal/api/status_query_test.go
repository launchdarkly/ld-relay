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
		res, code := EvaluateExpectations(body, []string{"status=healthy"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
		require.Len(t, res.Results, 1)
		assert.True(t, res.Results[0].OK)
		assert.Equal(t, "healthy", res.Results[0].Actual)
		assert.Empty(t, res.Results[0].Problem)
	})

	t.Run("nested path holds", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.My Project my-env.connectionStatus.state=VALID"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("not-equals clause holds", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"status!=degraded"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("all clauses AND-ed", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{
			"status=healthy",
			"environments.My Project my-env.status=connected",
		}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
		assert.Len(t, res.Results, 2)
	})

	t.Run("boolean scalar", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.My Project my-env.bigSegmentStatus.available=true"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})

	t.Run("numeric scalar rendered without exponent", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.My Project my-env.connectionStatus.stateSince=1600000000000"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, res.Satisfied)
	})
}

func TestEvaluateExpectationsUnsatisfied(t *testing.T) {
	body := statusRepBody(t)

	t.Run("mismatch yields 412", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"status=degraded"}, SchemaAllEnvironments)
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
		}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Satisfied)
		assert.True(t, res.Results[0].OK)
		assert.False(t, res.Results[1].OK)
	})

	// An environment that is not configured is the single most common real assertion, and it must
	// stay a 412: the caller's path is addressable, the environment simply is not there.
	t.Run("unconfigured environment is unsatisfied, not 422", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.no-such-env.status=connected"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Results[0].OK)
		assert.Empty(t, res.Results[0].Problem)
	})

	t.Run("unconfigured environment is unsatisfied even for not-equals", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.no-such-env.status!=connected"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Satisfied)
		assert.False(t, res.Results[0].OK)
		assert.Empty(t, res.Results[0].Actual)
	})

	// These fields are in the schema but are omitted unless they have a value, so asserting on one
	// is a well-formed question with the answer "no".
	for _, clause := range []string{
		"environments.My Project my-env.envName=whatever",
		"environments.My Project my-env.expiringSdkKey=sdk-***",
		"environments.My Project my-env.dataStoreStatus.dbServer=localhost",
	} {
		t.Run("omitted optional field is unsatisfied: "+clause, func(t *testing.T) {
			res, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
			assert.Equal(t, http.StatusPreconditionFailed, code)
			assert.False(t, res.Results[0].OK)
			assert.Empty(t, res.Results[0].Problem)
		})
	}

	// bigSegmentStatus is absent on an environment that has no big segment store configured.
	t.Run("absent optional object is unsatisfied", func(t *testing.T) {
		res, code := EvaluateExpectations(envRepBody(t),
			[]string{"bigSegmentStatus.available=true"}, SchemaSingleEnvironment)
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.False(t, res.Results[0].OK)
		assert.Empty(t, res.Results[0].Problem)
	})
}

// A clause that does not parse as <path><operator><value> is the caller's syntax error: 400.
func TestEvaluateExpectationsUnparseable(t *testing.T) {
	body := statusRepBody(t)

	for _, clause := range []string{
		"status",            // no operator at all
		"=healthy",          // no path
		"!=healthy",         // no path, not-equals
		"environments[.x=1", // unterminated bracket
		"a[]=1",             // empty bracket
		`a["]=1`,            // lone double-quote in bracket (must not panic)
		"a[']=1",            // lone single-quote in bracket (must not panic)
		`a["]`,              // lone double-quote, no operator after
		"a[-1]=1",           // negative array index
		"a[xyz]=1",          // bracket content that is neither a quoted key, index, nor filter
		"a[=1]=1",           // filter with no field name
	} {
		t.Run(clause, func(t *testing.T) {
			res, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.False(t, res.Satisfied)
			require.Len(t, res.Results, 1)
			assert.False(t, res.Results[0].OK)
			assert.NotEmpty(t, res.Results[0].Problem)
		})
	}
}

// A clause that parses but names an operator or a field the relay cannot evaluate is a caller error
// too, but a distinguishable one: 422. Reporting these separately from 412 is what lets a caller
// tell a typo from an assertion that legitimately does not hold.
func TestEvaluateExpectationsNotEvaluable(t *testing.T) {
	body := statusRepBody(t)

	// Every unsupported operator must be reported as an operator problem. Previously the ones
	// containing "=" were silently absorbed into the path or the value and answered 412, and the
	// ones that did not were reported as a missing operator.
	t.Run("unsupported operator", func(t *testing.T) {
		for _, clause := range []string{
			"status>=healthy",
			"status<=healthy",
			"status>healthy",
			"status<healthy",
			"status==healthy",
			"status=~healthy",
			"status~=healthy",
			"status<>healthy",
			"status!healthy",
		} {
			t.Run(clause, func(t *testing.T) {
				res, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
				assert.Equal(t, http.StatusUnprocessableEntity, code)
				require.Len(t, res.Results, 1)
				assert.False(t, res.Results[0].OK)
				assert.Contains(t, res.Results[0].Problem, "is not supported")
				// The operator must not have leaked into a comparison.
				assert.Empty(t, res.Results[0].Expected)
				assert.Empty(t, res.Results[0].Actual)
			})
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		for _, clause := range []string{
			"connexionStatus.state=VALID",
			"statuss=healthy",
			"environments.My Project my-env.statuz=connected",
			"environments.My Project my-env.connectionStatus.stat=VALID",
			"a]b=c", // a stray ']' leaves a path that is not a field, and must not crash
		} {
			t.Run(clause, func(t *testing.T) {
				res, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
				assert.Equal(t, http.StatusUnprocessableEntity, code)
				require.Len(t, res.Results, 1)
				assert.Contains(t, res.Results[0].Problem, "unknown field")
			})
		}
	})

	t.Run("path landing on an object cannot be compared", func(t *testing.T) {
		for _, clause := range []string{
			"environments=anything",
			"environments.My Project my-env=anything",
			"environments.My Project my-env.connectionStatus=anything",
		} {
			t.Run(clause, func(t *testing.T) {
				res, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
				assert.Equal(t, http.StatusUnprocessableEntity, code)
				assert.Contains(t, res.Results[0].Problem, "not a single value")
			})
		}
	})

	t.Run("array syntax against something that is not an array", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments[0].status=connected"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.NotEmpty(t, res.Results[0].Problem)
	})

	t.Run("field addressed on a scalar", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{"status.nested=healthy"}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.NotEmpty(t, res.Results[0].Problem)
	})

	// The path grammar supports arrays so that selectors survive the concurrent-keys change, but
	// the current schema has no array fields, so addressing one is reported rather than silently
	// answered "not in the state you asserted".
	t.Run("array fields the schema does not have yet", func(t *testing.T) {
		for _, clause := range []string{
			"sdkKeys[0].key=x",
			"environments.My Project my-env.sdkKeys[key=new-production-default].value=x",
		} {
			t.Run(clause, func(t *testing.T) {
				_, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
				assert.Equal(t, http.StatusUnprocessableEntity, code)
			})
		}
	})

	// Paths are relative to the body of the route that serves them, so the wrapper only exists on
	// the all-environments route.
	t.Run("wrong root for the route", func(t *testing.T) {
		res, code := EvaluateExpectations(envRepBody(t),
			[]string{"environments.My Project my-env.status=connected"}, SchemaSingleEnvironment)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Contains(t, res.Results[0].Problem, "unknown field")
	})
}

// Every clause is evaluated and reported, so one request tells the caller about every problem, and
// the response code is the most serious outcome across all of them.
func TestEvaluateExpectationsReportsEveryClause(t *testing.T) {
	body := statusRepBody(t)

	t.Run("unparseable outranks not-evaluable and unsatisfied", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{
			"status",                      // 400
			"connexionStatus.state=VALID", // 422
			"status=degraded",             // 412
			"version=9.0.0",               // 200
		}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.False(t, res.Satisfied)
		require.Len(t, res.Results, 4)
		assert.NotEmpty(t, res.Results[0].Problem)
		assert.NotEmpty(t, res.Results[1].Problem)
		assert.Empty(t, res.Results[2].Problem)
		assert.False(t, res.Results[2].OK)
		assert.True(t, res.Results[3].OK)
	})

	t.Run("not-evaluable outranks unsatisfied", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{
			"status=degraded",             // 412
			"connexionStatus.state=VALID", // 422
		}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Len(t, res.Results, 2)
	})

	t.Run("unsatisfied outranks satisfied", func(t *testing.T) {
		res, code := EvaluateExpectations(body, []string{
			"version=9.0.0",
			"status=degraded",
		}, SchemaAllEnvironments)
		assert.Equal(t, http.StatusPreconditionFailed, code)
		assert.Len(t, res.Results, 2)
	})
}

func TestEvaluateExpectationsSingleEnvBody(t *testing.T) {
	body := envRepBody(t)

	// Against a single-environment body, paths are relative to the environment object itself --
	// no "environments" wrapper.
	res, code := EvaluateExpectations(body, []string{
		"status=connected",
		"connectionStatus.state=VALID",
	}, SchemaSingleEnvironment)
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, res.Satisfied)
}

func TestEvaluateExpectationsBracketQuotedKey(t *testing.T) {
	// An environment display name containing a dot must be addressable via bracket-quoting, since a
	// bare dot would be read as a path separator.
	rep := StatusRep{
		Status: "healthy",
		Environments: map[string]EnvironmentStatusRep{
			"proj.env": {Status: "connected"},
		},
	}
	body, err := json.Marshal(rep)
	require.NoError(t, err)

	for _, clause := range []string{
		`environments["proj.env"].status=connected`,
		`environments['proj.env'].status=connected`,
	} {
		t.Run(clause, func(t *testing.T) {
			res, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
			assert.Equal(t, http.StatusOK, code)
			assert.True(t, res.Satisfied)
		})
	}

	t.Run("unquoted dotted key is read as two steps and does not resolve", func(t *testing.T) {
		res, code := EvaluateExpectations(body,
			[]string{"environments.proj.env.status=connected"}, SchemaAllEnvironments)
		// "proj" is an addressable (absent) environment key, so "env" is checked as a field of
		// EnvironmentStatusRep and reported as unknown.
		assert.Equal(t, http.StatusUnprocessableEntity, code)
		assert.Contains(t, res.Results[0].Problem, "unknown field")
	})
}

// The documented selectors must keep working: this fails if a rep field is renamed without the
// documentation being updated.
func TestValidatePathAcceptsDocumentedSelectors(t *testing.T) {
	t.Run("all environments", func(t *testing.T) {
		body := statusRepBody(t)
		for _, clause := range []string{
			"status=healthy",
			"version=9.0.0",
			"clientVersion=7.15.2",
			"environments.My Project my-env.status=connected",
			"environments.My Project my-env.connectionStatus.state=VALID",
			"environments.My Project my-env.connectionStatus.stateSince=1600000000000",
			"environments.My Project my-env.dataStoreStatus.state=VALID",
			"environments.My Project my-env.bigSegmentStatus.available=true",
			"environments.My Project my-env.bigSegmentStatus.potentiallyStale=false",
			"environments.My Project my-env.sdkKey=sdk-***",
			"environments.My Project my-env.envId=env-123",
			"environments.My Project my-env.envKey=my-env",
			"environments.My Project my-env.projKey=my-proj",
		} {
			t.Run(clause, func(t *testing.T) {
				_, code := EvaluateExpectations(body, []string{clause}, SchemaAllEnvironments)
				assert.Equal(t, http.StatusOK, code, "documented selector must resolve and hold")
			})
		}
	})

	t.Run("single environment", func(t *testing.T) {
		body := envRepBody(t)
		for _, clause := range []string{
			"status=connected",
			"connectionStatus.state=VALID",
			"dataStoreStatus.state=VALID",
			"sdkKey=sdk-***",
			"envId=env-123",
		} {
			t.Run(clause, func(t *testing.T) {
				_, code := EvaluateExpectations(body, []string{clause}, SchemaSingleEnvironment)
				assert.Equal(t, http.StatusOK, code, "documented selector must resolve and hold")
			})
		}
	})
}

// The path grammar and the document walk are independent of the schema check, so the array forms are
// exercised directly. This keeps them covered until the concurrent-keys arrays land in the reps.
func TestPathGrammarArrayAccess(t *testing.T) {
	var doc interface{}
	require.NoError(t, json.Unmarshal([]byte(`{"sdkKeys":[
		{"key":"old-default","value":"sdk-aaa"},
		{"key":"new-production-default","value":"sdk-bbb"}]}`), &doc))

	walk := func(t *testing.T, path string) (interface{}, bool) {
		t.Helper()
		steps, problem := parsePath(path)
		require.Nil(t, problem)
		return walkPath(doc, steps)
	}

	t.Run("by index", func(t *testing.T) {
		v, found := walk(t, "sdkKeys[0].key")
		assert.True(t, found)
		assert.Equal(t, "old-default", v)
	})

	t.Run("by field filter", func(t *testing.T) {
		v, found := walk(t, "sdkKeys[key=new-production-default].value")
		assert.True(t, found)
		assert.Equal(t, "sdk-bbb", v)
	})

	t.Run("quoted filter value", func(t *testing.T) {
		v, found := walk(t, `sdkKeys[key="old-default"].value`)
		assert.True(t, found)
		assert.Equal(t, "sdk-aaa", v)
	})

	t.Run("filter with no match does not resolve", func(t *testing.T) {
		_, found := walk(t, "sdkKeys[key=missing].value")
		assert.False(t, found)
	})

	t.Run("index out of range does not resolve", func(t *testing.T) {
		_, found := walk(t, "sdkKeys[5].key")
		assert.False(t, found)
	})

	// A large index must not allocate; it simply does not resolve.
	t.Run("very large index does not resolve", func(t *testing.T) {
		_, found := walk(t, "sdkKeys[999999999].key")
		assert.False(t, found)
	})
}

func TestScalarToString(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    interface{}
		expected string
		scalar   bool
	}{
		{"string", "VALID", "VALID", true},
		{"true", true, "true", true},
		{"false", false, "false", true},
		{"whole number has no exponent", float64(1600000000000), "1600000000000", true},
		{"fractional number", 1.5, "1.5", true},
		{"explicit null", nil, "null", true},
		{"object", map[string]interface{}{"a": 1}, "", false},
		{"array", []interface{}{1}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, scalar := scalarToString(tc.value)
			assert.Equal(t, tc.expected, s)
			assert.Equal(t, tc.scalar, scalar)
		})
	}
}

func TestEvaluateExpectationsUnparseableBody(t *testing.T) {
	res, code := EvaluateExpectations([]byte("not json"), []string{"status=healthy"}, SchemaAllEnvironments)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.False(t, res.Satisfied)
	assert.NotEmpty(t, res.Error)
}

// URL.Query() drops a query segment it cannot parse and discards the error, which would turn a
// clause the relay cannot decode into no clause at all: a 200 with the full status document for an
// assertion that was never evaluated. The clauses have to be read in a way that can tell those
// apart.
func TestParseExpectQuery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		clauses   []string
		requested bool
		malformed bool
	}{
		{name: "empty query", raw: ""},
		{name: "unrelated parameter", raw: "foo=1"},
		{
			name: "one clause", raw: "expect=status=healthy",
			clauses: []string{"status=healthy"}, requested: true,
		},
		{
			name: "repeated clauses", raw: "expect=a=1&expect=b=2",
			clauses: []string{"a=1", "b=2"}, requested: true,
		},
		{
			name: "bare parameter with no value is still a request",
			raw:  "expect", clauses: []string{""}, requested: true,
		},
		// A raw ';' makes Go reject the segment holding it.
		{
			name: "sole clause holding a raw semicolon",
			raw:  "expect=status=degraded;x", requested: true, malformed: true,
		},
		{
			name: "sole clause holding a bad percent escape",
			raw:  "expect=status%3Ddegraded%zz", requested: true, malformed: true,
		},
		// The surviving clause must not be judged on its own: the dropped one asserted something
		// too, and answering 200 on the remainder is the fail-open this guards against.
		{
			name:    "one droppable clause among valid ones",
			raw:     "expect=status=healthy&expect=status=degraded;x",
			clauses: []string{"status=healthy"}, requested: true, malformed: true,
		},
		{
			name:    "unrelated parameter is the malformed one",
			raw:     "expect=status=healthy&junk=%zz",
			clauses: []string{"status=healthy"}, requested: true, malformed: true,
		},
		// A percent-encoded parameter name still identifies the request, so a dropped clause cannot
		// hide behind the encoding.
		{
			name: "percent-encoded parameter name",
			raw:  "%65xpect=status=degraded;x", requested: true, malformed: true,
		},
		// A malformed query that never mentions the parameter leaves the full-document path alone.
		{name: "malformed query without the parameter", raw: "junk=%zz", malformed: true},
		{name: "semicolon in an unrelated parameter", raw: "cachebust=1;2", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clauses, requested, malformed := ParseExpectQuery(tc.raw)
			assert.Equal(t, tc.clauses, clauses)
			assert.Equal(t, tc.requested, requested, "requested")
			assert.Equal(t, tc.malformed, malformed, "malformed")
		})
	}
}
