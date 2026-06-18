package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The status endpoints accept an "expect" query parameter so that callers can validate the
// health of the relay server-side and branch on an HTTP status code, instead of fetching the
// JSON body and parsing it themselves (e.g. with jq). Each "expect" clause is a path into the
// response body, a comparison operator, and an expected value:
//
//	/status?expect=status=healthy
//	/status/my-proj/my-env?expect=connectionStatus.state=VALID
//
// The parameter is repeatable; all clauses must hold for the request to be considered satisfied.
// This is deliberately a small, bounded grammar rather than a general query language: the status
// endpoints are unauthenticated, so an open-ended evaluator would be an unbounded compute surface.

// ExpectationResult is the outcome of evaluating a single "expect" clause.
type ExpectationResult struct {
	// Expr is the original clause as supplied by the caller.
	Expr string `json:"expr"`
	// Expected is the value the clause asserted.
	Expected string `json:"expected"`
	// Actual is the value found at the clause's path, rendered as a string. It is empty when the
	// path did not resolve to a scalar.
	Actual string `json:"actual"`
	// OK reports whether the clause held.
	OK bool `json:"ok"`
}

// ExpectationsResult is the body returned when a status request includes "expect" clauses.
type ExpectationsResult struct {
	// Satisfied reports whether every clause held.
	Satisfied bool `json:"satisfied"`
	// Results contains one entry per clause, in the order supplied.
	Results []ExpectationResult `json:"results,omitempty"`
	// Error describes why the query was rejected. It is set only for malformed queries.
	Error string `json:"error,omitempty"`
}

// EvaluateExpectations evaluates the "expect" clauses against a marshaled status body and returns
// the per-clause results together with the HTTP status code the handler should write:
//
//   - http.StatusOK (200) when every clause holds.
//   - http.StatusPreconditionFailed (412) when at least one clause is well-formed but does not
//     hold. A clause whose path is absent from the body is treated as unsatisfied: if the field
//     the caller asserted about is not even present, the relay is not in the state they assumed.
//   - http.StatusBadRequest (400) when any clause is malformed.
//
// body is the JSON the handler would otherwise have written, so a clause path matches exactly
// what the caller sees in the response.
func EvaluateExpectations(body []byte, clauses []string) (ExpectationsResult, int) {
	var doc interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		// The body is the relay's own freshly-marshaled output, so this is not reachable in
		// practice; treat it as a server error rather than blaming the caller's query.
		return ExpectationsResult{Satisfied: false, Error: "could not parse status body"},
			http.StatusInternalServerError
	}

	result := ExpectationsResult{Satisfied: true}
	for _, clause := range clauses {
		path, op, expected, err := parseClause(clause)
		if err != nil {
			return ExpectationsResult{
				Satisfied: false,
				Error:     fmt.Sprintf("invalid expect clause %q: %s", clause, err),
			}, http.StatusBadRequest
		}
		steps, err := parsePath(path)
		if err != nil {
			return ExpectationsResult{
				Satisfied: false,
				Error:     fmt.Sprintf("invalid expect clause %q: %s", clause, err),
			}, http.StatusBadRequest
		}

		actual, found := walkPath(doc, steps)
		actualStr, isScalar := scalarToString(actual)

		ok := false
		switch {
		case !found || !isScalar:
			// An absent field, or a path that lands on an object/array rather than a scalar,
			// cannot satisfy the clause. Reported as unsatisfied (412), not malformed (400):
			// the query is well-formed, the response simply is not in the asserted state.
			ok = false
			actualStr = ""
		case op == opEquals:
			ok = actualStr == expected
		case op == opNotEquals:
			ok = actualStr != expected
		}

		if !ok {
			result.Satisfied = false
		}
		result.Results = append(result.Results, ExpectationResult{
			Expr:     clause,
			Expected: expected,
			Actual:   actualStr,
			OK:       ok,
		})
	}

	if result.Satisfied {
		return result, http.StatusOK
	}
	return result, http.StatusPreconditionFailed
}

const (
	opEquals    = "="
	opNotEquals = "!="
)

// parseClause splits a clause into its path, operator, and expected value. The operator is the
// first "=" or "!=" found outside of any "[...]" brackets, so that an array filter such as
// sdkKeys[key=foo].value is not mistaken for the clause operator.
func parseClause(clause string) (path, op, value string, err error) {
	depth := 0
	for i := 0; i < len(clause); i++ {
		switch clause[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '!':
			if depth == 0 && i+1 < len(clause) && clause[i+1] == '=' {
				return clause[:i], opNotEquals, clause[i+2:], validateClauseParts(clause[:i])
			}
		case '=':
			if depth == 0 {
				return clause[:i], opEquals, clause[i+1:], validateClauseParts(clause[:i])
			}
		}
	}
	return "", "", "", fmt.Errorf("missing '=' or '!=' operator")
}

func validateClauseParts(path string) error {
	if path == "" {
		return fmt.Errorf("missing path before operator")
	}
	return nil
}

const (
	stepKey = iota
	stepIndex
	stepFilter
)

// pathStep is one segment of a parsed path: a map key, an array index, or an array field-filter.
type pathStep struct {
	kind  int
	key   string // stepKey, and the field name for stepFilter
	index int    // stepIndex
	value string // stepFilter: the value the field must equal
}

// parsePath parses a dotted path into ordered steps. Supported syntax:
//
//	a.b.c                    map keys
//	a["b.c"] / a['b.c']      bracket-quoted key (for keys containing dots or other punctuation)
//	a[0]                     array index
//	a[field=value]           first array element whose field equals value
//
// The bracket forms allow the path to address the concurrent-keys arrays that the status
// representation will grow (e.g. sdkKeys[key=new-production-default].value).
func parsePath(path string) ([]pathStep, error) {
	var steps []pathStep
	i := 0
	for i < len(path) {
		switch path[i] {
		case '.':
			i++
		case '[':
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated '[' in path")
			}
			step, err := parseBracket(path[i+1 : i+end])
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			i += end + 1
		default:
			start := i
			for i < len(path) && path[i] != '.' && path[i] != '[' {
				i++
			}
			steps = append(steps, pathStep{kind: stepKey, key: path[start:i]})
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	return steps, nil
}

func parseBracket(inner string) (pathStep, error) {
	if inner == "" {
		return pathStep{}, fmt.Errorf("empty '[]' in path")
	}
	// Quoted key: ["foo"] or ['foo'].
	if (inner[0] == '"' && inner[len(inner)-1] == '"') ||
		(inner[0] == '\'' && inner[len(inner)-1] == '\'') {
		return pathStep{kind: stepKey, key: inner[1 : len(inner)-1]}, nil
	}
	// Field filter: [field=value].
	if eq := strings.IndexByte(inner, '='); eq >= 0 {
		field := inner[:eq]
		if field == "" {
			return pathStep{}, fmt.Errorf("missing field name in filter")
		}
		return pathStep{kind: stepFilter, key: field, value: unquote(inner[eq+1:])}, nil
	}
	// Array index: [0].
	if idx, err := strconv.Atoi(inner); err == nil {
		if idx < 0 {
			return pathStep{}, fmt.Errorf("negative array index %d", idx)
		}
		return pathStep{kind: stepIndex, index: idx}, nil
	}
	return pathStep{}, fmt.Errorf("unrecognized '[%s]' in path", inner)
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// walkPath follows steps into a decoded JSON document, returning the value reached and whether the
// path resolved.
func walkPath(doc interface{}, steps []pathStep) (interface{}, bool) {
	cur := doc
	for _, step := range steps {
		switch step.kind {
		case stepKey:
			m, ok := cur.(map[string]interface{})
			if !ok {
				return nil, false
			}
			cur, ok = m[step.key]
			if !ok {
				return nil, false
			}
		case stepIndex:
			arr, ok := cur.([]interface{})
			if !ok || step.index >= len(arr) {
				return nil, false
			}
			cur = arr[step.index]
		case stepFilter:
			arr, ok := cur.([]interface{})
			if !ok {
				return nil, false
			}
			match, found := filterArray(arr, step.key, step.value)
			if !found {
				return nil, false
			}
			cur = match
		}
	}
	return cur, true
}

func filterArray(arr []interface{}, field, value string) (interface{}, bool) {
	for _, el := range arr {
		m, ok := el.(map[string]interface{})
		if !ok {
			continue
		}
		fv, ok := m[field]
		if !ok {
			continue
		}
		if s, isScalar := scalarToString(fv); isScalar && s == value {
			return el, true
		}
	}
	return nil, false
}

// scalarToString renders a decoded JSON scalar as the string it is compared against. It reports
// false for objects and arrays, which cannot be compared as scalars.
func scalarToString(v interface{}) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		// JSON numbers decode to float64; render whole numbers (e.g. millisecond timestamps)
		// without a decimal point or exponent.
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case nil:
		return "null", true
	default:
		return "", false
	}
}
