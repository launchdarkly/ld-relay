// Event payload regression tests for concurrent-keys Phase 1.
//
// Purpose: catch accidental schema drift in upstream event payloads across the lifetime
// of the concurrent-keys project. Every sub-PR that touches event-related code must keep
// these green.
//
// Invariants tested:
//  1. Analytics payloads use the anchor credential (EventDispatcher's stored authKey) in the
//     Authorization header — not the credential on the incoming request.
//  2. Diagnostic payloads proxy the incoming request's Authorization header verbatim.
//  3. After a re-anchor (ReplaceCredential), analytics uses the new anchor; diagnostic is unchanged.
//  4. Body schemas match the v8 baseline fixtures in testdata/.
//
// REFRESHING THE BASELINE: see docs/concurrent-keys/baseline-refresh.md
package events

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadPayloadFixture reads a baseline fixture file from testdata/.
func loadPayloadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err, "loading payload fixture %s", name)
	return data
}

// jsonSchemaOf returns a canonical string describing the structure of a JSON value: its
// field names (for objects) and nesting, not its values. Used to detect schema drift
// without being sensitive to value changes (timestamps, IDs, etc.).
//
// Object key order is normalised alphabetically so comparisons are deterministic.
// Array schemas reflect only the first element (relay event arrays are homogeneous by kind;
// the schema of element[0] is representative).
func jsonSchemaOf(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		if len(val) == 0 {
			return "[]"
		}
		return "[" + jsonSchemaOf(val[0]) + "]"
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+jsonSchemaOf(val[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		return fmt.Sprintf("unknown(%T)", v)
	}
}

// assertBodySchemaMatchesFixture parses both byte slices as JSON and asserts they have
// the same structure — key names and nesting — ignoring values and field ordering.
func assertBodySchemaMatchesFixture(t *testing.T, fixture, actual []byte) {
	t.Helper()
	var fixtureVal, actualVal any
	require.NoError(t, json.Unmarshal(fixture, &fixtureVal), "fixture must be valid JSON")
	require.NoError(t, json.Unmarshal(actual, &actualVal), "upstream body must be valid JSON")
	assert.Equal(t, jsonSchemaOf(fixtureVal), jsonSchemaOf(actualVal),
		"upstream body JSON schema must match v8 baseline fixture")
}

// expectUpstreamRequest waits for one upstream request to arrive on requestsCh.
func expectUpstreamRequest(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) httphelpers.HTTPRequestInfo {
	t.Helper()
	return helpers.RequireValue(t, requestsCh, time.Second)
}

// TestEventPayloadRegressionVerbatimAnalytics verifies that verbatim analytics payloads
// (schema v4, modern Go SDK path) are forwarded upstream with:
//   - the anchor credential in Authorization (not the incoming request's credential), and
//   - a body schema identical to the v8 baseline fixture.
func TestEventPayloadRegressionVerbatimAnalytics(t *testing.T) {
	fixture := loadPayloadFixture(t, "baseline_analytics_verbatim.json")

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		headers := headersWithEventSchema(CurrentEventsSchemaVersion)
		// Simulate an incoming request from a non-anchor SDK key.  The upstream output must
		// use the anchor (EventDispatcher's stored authKey), not this incoming credential.
		headers.Set("Authorization", "sdk-non-anchor-key-must-not-reach-upstream")

		req := st.BuildRequest("POST", "/", fixture, headers)
		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, req)
		assert.Equal(t, 202, w.Result().StatusCode)

		p.dispatcher.flush()
		r := expectUpstreamRequest(t, p.requestsCh)

		// Invariant 1: analytics upstream must carry the anchor credential.
		assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"analytics upstream must use anchor credential, not the incoming request credential")

		// Invariant 4: body schema must match v8 baseline.
		body, err := util.DecompressGzipData(r.Body)
		require.NoError(t, err)
		assertBodySchemaMatchesFixture(t, fixture, body)
	})
}

// TestEventPayloadRegressionDiagnostic verifies that diagnostic payloads are forwarded
// upstream with:
//   - the original incoming Authorization header (verbatim, not the anchor), and
//   - a body schema identical to the v8 baseline fixture.
//
// Per design §4.3: diagnostic events preserve the original credential for debug attribution
// ("which SDK reported this"), while analytics events collapse to the anchor.
func TestEventPayloadRegressionDiagnostic(t *testing.T) {
	fixture := loadPayloadFixture(t, "baseline_diagnostic.json")
	const originalAuth = "sdk-original-diagnostic-client-auth"

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		headers := headersWithEventSchema(0)
		headers.Set("Authorization", originalAuth)

		req := st.BuildRequest("POST", "/", fixture, headers)
		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, req)
		assert.Equal(t, 202, w.Result().StatusCode)

		r := expectUpstreamRequest(t, p.requestsCh)

		// Invariant 2: diagnostic upstream must carry the original auth, not the anchor.
		assert.Equal(t, originalAuth, r.Request.Header.Get("Authorization"),
			"diagnostic upstream must proxy the original authorization header verbatim")
		assert.NotEqual(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"diagnostic upstream must NOT use the anchor credential")

		// Invariant 4: body schema must match v8 baseline.
		body, err := util.DecompressGzipData([]byte(r.Body))
		require.NoError(t, err)
		assertBodySchemaMatchesFixture(t, fixture, body)
	})
}

// TestEventPayloadRegressionSummarizedAnalytics verifies that pre-summarization analytics
// payloads (PHP SDK path: schema ≤2 or Unsummarized header) are forwarded upstream with:
//   - the anchor credential in Authorization, and
//   - a body that is a valid summarized event array with the expected schema.
func TestEventPayloadRegressionSummarizedAnalytics(t *testing.T) {
	inputFixture := loadPayloadFixture(t, "baseline_analytics_summarize_input.json")

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		headers := headersWithEventSchema(2)
		headers.Set(EventUnsummarizedHeader, "true")

		req := st.BuildRequest("POST", "/", inputFixture, headers)
		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, req)
		assert.Equal(t, 202, w.Result().StatusCode)

		p.dispatcher.flush()
		r := expectUpstreamRequest(t, p.requestsCh)

		// Invariant 1: summarized analytics upstream must carry the anchor credential.
		assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"summarized analytics upstream must use anchor credential")

		// Invariant 4: output must be a valid JSON array of event objects; final event is summary.
		body, err := util.DecompressGzipData(r.Body)
		require.NoError(t, err)

		var events []map[string]any
		require.NoError(t, json.Unmarshal(body, &events), "summarized output must be a JSON array of objects")
		require.NotEmpty(t, events, "summarized output must not be empty")

		for i, evt := range events {
			_, hasKind := evt["kind"]
			assert.True(t, hasKind, "event[%d] must have a 'kind' field", i)
		}
		// The summarizing relay always appends a summary event last.
		lastEvt := events[len(events)-1]
		assert.Equal(t, "summary", lastEvt["kind"], "last event must be a summary")
		_, hasFeatures := lastEvt["features"]
		assert.True(t, hasFeatures, "summary event must have a 'features' field")
	})
}

// TestEventPayloadRegressionAnchorReplacement is the core Phase 1 regression test.
// It simulates the re-anchor operation (T2.c) and verifies credential routing remains
// correct before and after ReplaceCredential is called.
//
// Invariant 3: after re-anchor, analytics uses the new anchor; diagnostic stays verbatim.
func TestEventPayloadRegressionAnchorReplacement(t *testing.T) {
	analyticsFixture := loadPayloadFixture(t, "baseline_analytics_verbatim.json")
	diagnosticFixture := loadPayloadFixture(t, "baseline_diagnostic.json")

	newAnchorKey := config.SDKKey(string(st.EnvMain.Config.SDKKey) + "-rotated")
	const diagnosticAuth = "sdk-original-client-that-sent-diagnostic"

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		analyticsHandler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		diagnosticHandler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind)
		require.NotNil(t, analyticsHandler)
		require.NotNil(t, diagnosticHandler)

		sendAnalytics := func() httphelpers.HTTPRequestInfo {
			analyticsHandler(
				httptest.NewRecorder(),
				st.BuildRequest("POST", "/", analyticsFixture, headersWithEventSchema(CurrentEventsSchemaVersion)),
			)
			p.dispatcher.flush()
			return expectUpstreamRequest(t, p.requestsCh)
		}

		sendDiagnostic := func() httphelpers.HTTPRequestInfo {
			diagHeaders := headersWithEventSchema(0)
			diagHeaders.Set("Authorization", diagnosticAuth)
			diagnosticHandler(
				httptest.NewRecorder(),
				st.BuildRequest("POST", "/", diagnosticFixture, diagHeaders),
			)
			return expectUpstreamRequest(t, p.requestsCh)
		}

		// --- Before re-anchor ---
		r := sendAnalytics()
		assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"before re-anchor: analytics must use original anchor")

		// --- Simulate Phase 1 re-anchor (T2.c calls ReplaceCredential) ---
		p.dispatcher.ReplaceCredential(newAnchorKey)

		// --- After re-anchor: analytics must switch to new anchor ---
		r = sendAnalytics()
		assert.Equal(t, string(newAnchorKey), r.Request.Header.Get("Authorization"),
			"after re-anchor: analytics must use new anchor credential")
		assert.NotEqual(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"after re-anchor: analytics must NOT still use old anchor credential")

		// --- After re-anchor: diagnostic must still proxy the original auth verbatim ---
		r = sendDiagnostic()
		assert.Equal(t, diagnosticAuth, r.Request.Header.Get("Authorization"),
			"after re-anchor: diagnostic must still proxy original auth, not the new anchor")
		assert.NotEqual(t, string(newAnchorKey), r.Request.Header.Get("Authorization"),
			"after re-anchor: diagnostic must NOT use the new anchor credential")

		// Diagnostic body schema must be preserved across re-anchor.
		diagBody, err := util.DecompressGzipData(r.Body)
		require.NoError(t, err)
		assertBodySchemaMatchesFixture(t, diagnosticFixture, diagBody)
	})
}
