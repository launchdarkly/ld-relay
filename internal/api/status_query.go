package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
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
//
// A clause can fail in three different ways, and each gets its own status code so that a caller
// debugging a probe can tell them apart:
//
//   - The clause does not parse as <path><operator><value> at all: 400.
//   - The clause parses, but names an operator or a field the relay cannot evaluate: 422.
//   - The clause is evaluable and simply does not hold: 412.
//
// The distinction between 422 and 412 is drawn against the *schema* of the status document, not
// against the particular body being served. A misspelled field is a caller error and reports 422,
// while a real field that is merely absent right now -- an unconfigured environment, an omitted
// expiringSdkKey, a bigSegmentStatus on an environment without big segments -- is a legitimately
// unmet assertion and reports 412.

// ExpectParam is the query parameter that carries the assertion clauses.
const ExpectParam = "expect"

// ParseExpectQuery reads the "expect" clauses out of a raw URL query string. requested reports
// whether the caller used the parameter at all; malformed reports whether the query string could be
// parsed.
//
// This does not go through Request.URL.Query(), which discards the parse error from
// url.ParseQuery and drops only the offending segment. A clause the relay cannot decode would then
// look identical to no clause at all, and the handler would answer 200 with the full status document
// for an assertion it never evaluated. Go rejects a segment holding a raw ";" or a bad "%" escape,
// and hand-encoding is exactly what the bracket-quoted environment keys ask callers to do, so this
// is reachable from a single-character typo. A verdict endpoint has to fail closed, so the error is
// surfaced to the caller instead.
func ParseExpectQuery(rawQuery string) (clauses []string, requested, malformed bool) {
	values, err := url.ParseQuery(rawQuery)
	clauses = values[ExpectParam]
	requested = len(clauses) > 0
	if err != nil {
		malformed = true
		if !requested {
			// Every clause was dropped, so the surviving values cannot say whether the caller was
			// asking for a verdict. Look for the parameter in the raw query instead, to keep a
			// request that never mentioned it on the unchanged full-document path.
			requested = rawQueryHasParam(rawQuery, ExpectParam)
		}
	}
	return clauses, requested, malformed
}

// rawQueryHasParam reports whether the raw query string carries the named parameter, regardless of
// whether its value can be decoded.
func rawQueryHasParam(rawQuery, param string) bool {
	for rawQuery != "" {
		var segment string
		segment, rawQuery, _ = strings.Cut(rawQuery, "&")
		key, _, _ := strings.Cut(segment, "=")
		// A key that does not unescape is one this cannot identify, which is the same reason
		// url.ParseQuery dropped it.
		if decoded, err := url.QueryUnescape(key); err == nil && decoded == param {
			return true
		}
	}
	return false
}

// StatusSchema identifies which status document a set of clauses is evaluated against. Paths are
// relative to the body of the route that serves them, so the two routes validate against different
// roots.
type StatusSchema int

const (
	// SchemaAllEnvironments is the body of the /status route: a StatusRep.
	SchemaAllEnvironments StatusSchema = iota
	// SchemaSingleEnvironment is the body of the per-environment status routes: a bare
	// EnvironmentStatusRep, with no "environments" wrapper.
	SchemaSingleEnvironment
)

// rootType returns the Go type whose JSON encoding a path is checked against. The status handlers
// marshal exactly these types, so a path that this type cannot address is one the caller got wrong.
func (s StatusSchema) rootType() reflect.Type {
	if s == SchemaSingleEnvironment {
		return reflect.TypeOf(EnvironmentStatusRep{})
	}
	return reflect.TypeOf(StatusRep{})
}

// ExpectationResult is the outcome of evaluating a single "expect" clause.
type ExpectationResult struct {
	// Expr is the original clause as supplied by the caller.
	Expr string `json:"expr"`
	// Expected is the value the clause asserted. It is omitted for a clause that was not evaluated.
	Expected string `json:"expected,omitempty"`
	// Actual is the value found at the clause's path, rendered as a string. It is omitted for a
	// clause that was not evaluated, and empty when the path is absent from the body.
	Actual string `json:"actual,omitempty"`
	// Problem describes why the clause could not be evaluated. It is empty for a clause that was.
	Problem string `json:"problem,omitempty"`
	// OK reports whether the clause held. It is false for a clause that was not evaluated.
	OK bool `json:"ok"`
}

// ExpectationsResult is the body returned when a status request includes "expect" clauses.
type ExpectationsResult struct {
	// Satisfied reports whether every clause was evaluated and held.
	Satisfied bool `json:"satisfied"`
	// Results contains one entry per clause, in the order supplied.
	Results []ExpectationResult `json:"results,omitempty"`
	// Error describes why the whole query was abandoned, as opposed to a problem with one clause.
	Error string `json:"error,omitempty"`
}

// EvaluateExpectations evaluates the "expect" clauses against a marshaled status body and returns
// the per-clause results together with the HTTP status code the handler should write. Every clause
// is evaluated, so one request reports every problem the caller needs to fix; the response code is
// the most serious outcome across all of them:
//
//   - http.StatusBadRequest (400) when any clause does not parse.
//   - http.StatusUnprocessableEntity (422) when any clause parses but names an operator or a field
//     that cannot be evaluated against the schema.
//   - http.StatusPreconditionFailed (412) when every clause is evaluable but at least one does not
//     hold. A clause whose path is absent from the body is treated as unsatisfied: if the field the
//     caller asserted about is not even present, the relay is not in the state they assumed.
//   - http.StatusOK (200) when every clause holds.
//
// body is the JSON the handler would otherwise have written, so a clause path matches exactly
// what the caller sees in the response. schema says which document that is.
func EvaluateExpectations(body []byte, clauses []string, schema StatusSchema) (ExpectationsResult, int) {
	var doc interface{}
	if err := json.Unmarshal(body, &doc); err != nil {
		// The body is the relay's own freshly-marshaled output, so this is not reachable in
		// practice; treat it as a server error rather than blaming the caller's query.
		return ExpectationsResult{Satisfied: false, Error: "could not parse status body"},
			http.StatusInternalServerError
	}
	root := schema.rootType()

	result := ExpectationsResult{Satisfied: true}
	code := http.StatusOK
	for _, clause := range clauses {
		clauseResult, clauseCode := evaluateClause(doc, root, clause)
		if !clauseResult.OK {
			result.Satisfied = false
		}
		if severity(clauseCode) > severity(code) {
			code = clauseCode
		}
		result.Results = append(result.Results, clauseResult)
	}
	return result, code
}

// severity ranks the per-clause outcomes so that the response reports the most serious one: a clause
// that does not parse outranks one that cannot be evaluated, which outranks one that merely does not
// hold.
func severity(code int) int {
	switch code {
	case http.StatusBadRequest:
		return 3
	case http.StatusUnprocessableEntity:
		return 2
	case http.StatusPreconditionFailed:
		return 1
	default:
		return 0
	}
}

func evaluateClause(doc interface{}, root reflect.Type, clause string) (ExpectationResult, int) {
	path, op, expected, problem := parseClause(clause)
	if problem == nil {
		var steps []pathStep
		steps, problem = parsePath(path)
		if problem == nil {
			problem = validatePath(root, steps)
		}
		if problem == nil {
			return compare(doc, steps, op, expected, clause)
		}
	}
	return ExpectationResult{Expr: clause, Problem: problem.msg}, problem.code()
}

func compare(doc interface{}, steps []pathStep, op, expected, clause string) (ExpectationResult, int) {
	// A path that passed validation addresses a scalar, so anything found here renders as one.
	actual, found := walkPath(doc, steps)
	actualStr := ""
	if found {
		actualStr, _ = scalarToString(actual)
	}

	ok := false
	switch {
	case !found:
		// An absent field cannot satisfy the clause, for either operator: the caller asserted
		// something about a field the relay is not currently reporting.
	case op == opEquals:
		ok = actualStr == expected
	case op == opNotEquals:
		ok = actualStr != expected
	}

	result := ExpectationResult{Expr: clause, Expected: expected, Actual: actualStr, OK: ok}
	if ok {
		return result, http.StatusOK
	}
	return result, http.StatusPreconditionFailed
}

// clauseProblem is a reason a clause could not be evaluated, carrying the status code that reason
// maps to. It is deliberately a concrete type rather than an error: every producer returns it
// directly, so there is nothing to unwrap.
type clauseProblem struct {
	msg string
	// semantic distinguishes a clause the relay understood but cannot evaluate (422) from one it
	// could not parse (400).
	semantic bool
}

func (p *clauseProblem) code() int {
	if p.semantic {
		return http.StatusUnprocessableEntity
	}
	return http.StatusBadRequest
}

func syntaxProblem(format string, args ...interface{}) *clauseProblem {
	return &clauseProblem{msg: fmt.Sprintf(format, args...)}
}

func semanticProblem(format string, args ...interface{}) *clauseProblem {
	return &clauseProblem{msg: fmt.Sprintf(format, args...), semantic: true}
}

const (
	opEquals    = "="
	opNotEquals = "!="
)

// operatorChars are the characters that only ever form a comparison operator, and never part of an
// unquoted path. Excluding '-', '_' and ':' keeps them available to the environment names and keys
// that appear as map keys under "environments".
const operatorChars = "=!<>~"

// parseClause splits a clause into its path, operator, and expected value. The whole run of
// operator characters is read as one token, so an unsupported operator is reported as one instead of
// being absorbed into the path ("status>=x" asserting on a field named "status>") or into the value
// ("status==x" comparing against the literal "=x"). The scan ignores anything inside "[...]", so an
// array filter such as sdkKeys[key=foo].value is not mistaken for the clause operator.
func parseClause(clause string) (path, op, value string, problem *clauseProblem) {
	depth := 0
	for i := 0; i < len(clause); i++ {
		switch c := clause[i]; {
		case c == '[':
			depth++
		case c == ']':
			// Clamp at zero so an unbalanced ']' cannot drive depth negative and hide the
			// real top-level operator from the scan below.
			if depth > 0 {
				depth--
			}
		case depth == 0 && strings.IndexByte(operatorChars, c) >= 0:
			end := i
			for end < len(clause) && strings.IndexByte(operatorChars, clause[end]) >= 0 {
				end++
			}
			token := clause[i:end]
			if i == 0 {
				return "", "", "", syntaxProblem("missing path before the %s operator", strconv.Quote(token))
			}
			if token != opEquals && token != opNotEquals {
				return "", "", "", semanticProblem("operator %s is not supported; use %s or %s",
					strconv.Quote(token), strconv.Quote(opEquals), strconv.Quote(opNotEquals))
			}
			return clause[:i], token, clause[end:], nil
		}
	}
	return "", "", "", syntaxProblem(`missing "=" or "!=" operator`)
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

// label renders a step the way the caller wrote it, for use in a problem message.
func (s pathStep) label() string {
	switch s.kind {
	case stepIndex:
		return fmt.Sprintf("[%d]", s.index)
	case stepFilter:
		return fmt.Sprintf("[%s=%s]", s.key, s.value)
	default:
		return s.key
	}
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
func parsePath(path string) ([]pathStep, *clauseProblem) {
	var steps []pathStep
	i := 0
	for i < len(path) {
		switch path[i] {
		case '.':
			i++
		case '[':
			// The bracket ends at the first ']'. A key or filter value that itself contains ']'
			// is therefore not addressable; this is acceptable because status field values
			// (credentials, states, identifiers) do not contain ']'.
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				return nil, syntaxProblem("unterminated '[' in path")
			}
			step, problem := parseBracket(path[i+1 : i+end])
			if problem != nil {
				return nil, problem
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
		return nil, syntaxProblem("empty path")
	}
	return steps, nil
}

func parseBracket(inner string) (pathStep, *clauseProblem) {
	if inner == "" {
		return pathStep{}, syntaxProblem("empty '[]' in path")
	}
	// Quoted key: ["foo"] or ['foo']. The length guard ensures a lone quote character (where
	// inner[0] and inner[len-1] are the same byte) is not mistaken for a quoted key, which would
	// slice inner[1:0] and panic.
	if len(inner) >= 2 &&
		((inner[0] == '"' && inner[len(inner)-1] == '"') ||
			(inner[0] == '\'' && inner[len(inner)-1] == '\'')) {
		return pathStep{kind: stepKey, key: inner[1 : len(inner)-1]}, nil
	}
	// Field filter: [field=value].
	if eq := strings.IndexByte(inner, '='); eq >= 0 {
		field := inner[:eq]
		if field == "" {
			return pathStep{}, syntaxProblem("missing field name in filter")
		}
		return pathStep{kind: stepFilter, key: field, value: unquote(inner[eq+1:])}, nil
	}
	// Array index: [0].
	if idx, err := strconv.Atoi(inner); err == nil {
		if idx < 0 {
			return pathStep{}, syntaxProblem("negative array index %d", idx)
		}
		return pathStep{kind: stepIndex, index: idx}, nil
	}
	return pathStep{}, syntaxProblem("unrecognized '[%s]' in path", inner)
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// validatePath reports whether the steps address a comparable value in the status document that root
// describes. It answers "could this path ever resolve", not "does it resolve in this body": a field
// that exists but is currently omitted is a clause that does not hold, not a clause the relay cannot
// evaluate.
//
// The check walks the Go types the handlers marshal, matching each step against the json tags. A map
// accepts any key, which is what makes environments.<any environment>.<field> addressable while
// still checking <field>.
func validatePath(root reflect.Type, steps []pathStep) *clauseProblem {
	t := root
	for _, step := range steps {
		t = deref(t)
		switch t.Kind() {
		case reflect.Struct:
			if step.kind != stepKey {
				return semanticProblem("%s addresses an array, but that part of the status is an object",
					strconv.Quote(step.label()))
			}
			field, ok := jsonField(t, step.key)
			if !ok {
				return semanticProblem("unknown field %s", strconv.Quote(step.key))
			}
			t = field
		case reflect.Map:
			// Any key is addressable; the value type is what the remaining steps must match.
			if step.kind != stepKey {
				return semanticProblem("%s addresses an array, but that part of the status is a map",
					strconv.Quote(step.label()))
			}
			t = t.Elem()
		case reflect.Slice, reflect.Array:
			switch step.kind {
			case stepIndex:
				t = t.Elem()
			case stepFilter:
				if element := deref(t.Elem()); element.Kind() != reflect.Struct {
					return semanticProblem("cannot filter on field %s: the array does not hold objects",
						strconv.Quote(step.key))
				} else if _, ok := jsonField(element, step.key); !ok {
					return semanticProblem("unknown field %s", strconv.Quote(step.key))
				}
				t = t.Elem()
			default:
				return semanticProblem("%s addresses a field, but that part of the status is an array",
					strconv.Quote(step.key))
			}
		default:
			return semanticProblem("%s addresses a field of a value that has none",
				strconv.Quote(step.label()))
		}
	}
	if !isComparableKind(deref(t).Kind()) {
		return semanticProblem("%s is not a single value, so it cannot be compared",
			strconv.Quote(steps[len(steps)-1].label()))
	}
	return nil
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// isComparableKind reports whether a type marshals to a JSON scalar, which is what a clause can
// compare against. Every field of the status representations is a string, a bool, a number, or a
// named type over one of those; a field whose type marshals to a scalar by some other route (a
// custom MarshalJSON over a struct, say) would need handling here.
func isComparableKind(k reflect.Kind) bool {
	switch k {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// jsonField returns the type of the field that marshals under the given JSON name.
func jsonField(t reflect.Type, name string) (reflect.Type, bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // unexported, so it is not in the body
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		tagName, _, _ := strings.Cut(tag, ",")
		if tagName == "" {
			tagName = field.Name
		}
		if tagName == name {
			return field.Type, true
		}
	}
	return nil, false
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
