// Credential routing regression tests for the EventDispatcher.
//
// Invariants:
//  1. Analytics events are always forwarded upstream under the anchor credential
//     (EventDispatcher's stored authKey) — never under the credential that arrived
//     on the incoming SDK request.
//  2. Diagnostic events proxy the incoming request's Authorization header verbatim;
//     the anchor credential is not used.
//  3. After ReplaceCredential is called (anchor rotation), analytics switches to the
//     new anchor immediately; diagnostic forwarding is unaffected.
package events

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireUpstreamRequest(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) httphelpers.HTTPRequestInfo {
	t.Helper()
	return helpers.RequireValue(t, requestsCh, time.Second)
}

// TestAnalyticsUpstreamUsesAnchorCredential verifies that analytics events are forwarded
// upstream under the dispatcher's stored anchor credential, even when the incoming SDK
// request carries a different Authorization header.
func TestAnalyticsUpstreamUsesAnchorCredential(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		headers := headersWithEventSchema(CurrentEventsSchemaVersion)
		// Incoming request carries a non-anchor key — it must not reach the upstream.
		headers.Set("Authorization", "sdk-non-anchor-key-must-not-reach-upstream")

		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly), headers))
		assert.Equal(t, 202, w.Result().StatusCode)

		p.dispatcher.flush()
		r := requireUpstreamRequest(t, p.requestsCh)

		assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"analytics upstream must carry the anchor credential, not the incoming request credential")
	})
}

// TestDiagnosticUpstreamProxiesIncomingCredential verifies that diagnostic events proxy
// the incoming request's Authorization header verbatim to the upstream, not the anchor.
func TestDiagnosticUpstreamProxiesIncomingCredential(t *testing.T) {
	const sdkAuth = "sdk-original-diagnostic-client-auth"

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		headers := headersWithEventSchema(0)
		headers.Set("Authorization", sdkAuth)

		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly), headers))
		assert.Equal(t, 202, w.Result().StatusCode)

		r := requireUpstreamRequest(t, p.requestsCh)

		assert.Equal(t, sdkAuth, r.Request.Header.Get("Authorization"),
			"diagnostic upstream must proxy the incoming Authorization header verbatim")
		assert.NotEqual(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"diagnostic upstream must not use the anchor credential")
	})
}

// TestCredentialRoutingAfterReplaceCredential verifies that after ReplaceCredential is
// called (anchor rotation), analytics events use the new anchor while diagnostic events
// continue to proxy the original incoming authorization.
func TestCredentialRoutingAfterReplaceCredential(t *testing.T) {
	newAnchorKey := config.SDKKey(string(st.EnvMain.Config.SDKKey) + "-rotated")
	const sdkDiagAuth = "sdk-original-client-that-sent-diagnostic"

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		analyticsHandler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		diagnosticHandler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind)
		require.NotNil(t, analyticsHandler)
		require.NotNil(t, diagnosticHandler)

		sendAnalytics := func() httphelpers.HTTPRequestInfo {
			analyticsHandler(
				httptest.NewRecorder(),
				st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly), headersWithEventSchema(CurrentEventsSchemaVersion)),
			)
			p.dispatcher.flush()
			return requireUpstreamRequest(t, p.requestsCh)
		}

		sendDiagnostic := func() httphelpers.HTTPRequestInfo {
			headers := headersWithEventSchema(0)
			headers.Set("Authorization", sdkDiagAuth)
			diagnosticHandler(
				httptest.NewRecorder(),
				st.BuildRequest("POST", "/", []byte(eventPayloadForVerbatimOnly), headers),
			)
			// Diagnostic events are forwarded immediately; no flush needed.
			return requireUpstreamRequest(t, p.requestsCh)
		}

		// Before rotation: analytics uses the original anchor.
		r := sendAnalytics()
		assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"before rotation: analytics must use original anchor")

		// Rotate the anchor.
		p.dispatcher.ReplaceCredential(newAnchorKey)

		// After rotation: analytics must switch to the new anchor.
		r = sendAnalytics()
		assert.Equal(t, string(newAnchorKey), r.Request.Header.Get("Authorization"),
			"after rotation: analytics must use new anchor credential")
		assert.NotEqual(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"after rotation: analytics must not still use old anchor credential")

		// After rotation: diagnostic must still proxy the original incoming auth.
		r = sendDiagnostic()
		assert.Equal(t, sdkDiagAuth, r.Request.Header.Get("Authorization"),
			"after rotation: diagnostic must still proxy the original incoming authorization")
		assert.NotEqual(t, string(newAnchorKey), r.Request.Header.Get("Authorization"),
			"after rotation: diagnostic must not use the new anchor credential")
	})
}
