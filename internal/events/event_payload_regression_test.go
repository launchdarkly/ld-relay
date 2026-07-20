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
	"net/http"
	"net/http/httptest"
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

func requireUpstreamRequest(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) httphelpers.HTTPRequestInfo {
	t.Helper()
	return helpers.RequireValue(t, requestsCh, time.Second)
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

// headerSet builds a set of canonicalized header names for use as an allowed-delta list.
func headerSet(keys ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		s[http.CanonicalHeaderKey(k)] = struct{}{}
	}
	return s
}

// assertProxiedHeadersMatchExcept fails if any header present on either the incoming or the upstream
// request has a differing value, unless its (canonicalized) name is in allowedDelta. This pins that the
// forwarder passes headers through unchanged and diverges only on the enumerated transport/credential
// headers, catching any regression that silently drops, adds, or rewrites a header.
func assertProxiedHeadersMatchExcept(t *testing.T, incoming, upstream http.Header, allowedDelta map[string]struct{}) {
	t.Helper()
	checked := make(map[string]struct{})
	compare := func(h http.Header) {
		for name := range h {
			key := http.CanonicalHeaderKey(name)
			if _, done := checked[key]; done {
				continue
			}
			checked[key] = struct{}{}
			if _, skip := allowedDelta[key]; skip {
				continue
			}
			assert.Equalf(t, incoming.Get(key), upstream.Get(key),
				"header %q must be forwarded unchanged", key)
		}
	}
	compare(incoming)
	compare(upstream)
}

// TestAnalyticsUpstreamPayloadIsByteIdentical is the payload-regression form of the analytics routing
// test: beyond the credential decision, the forwarded body must be byte-for-byte identical to the
// incoming one, and every header must be forwarded unchanged except Authorization (swapped to the
// anchor) and the transport/compression headers the sender legitimately adds.
func TestAnalyticsUpstreamPayloadIsByteIdentical(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		incomingBody := []byte(eventPayloadForVerbatimOnly)
		incomingHeaders := headersWithEventSchema(CurrentEventsSchemaVersion)
		incomingHeaders.Set(TagsHeader, "application-id/my-app application-version/1.2.3")
		// The incoming request carries a non-anchor credential; it must not reach the upstream.
		incomingHeaders.Set("Authorization", "sdk-non-anchor-key-must-not-reach-upstream")

		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, st.BuildRequest("POST", "/", incomingBody, incomingHeaders))
		assert.Equal(t, 202, w.Result().StatusCode)

		p.dispatcher.flush()
		r := requireUpstreamRequest(t, p.requestsCh)

		// The upstream body is gzip-compressed; once decompressed it must equal the incoming bytes exactly.
		uncompressed, err := util.DecompressGzipData(r.Body)
		require.NoError(t, err)
		assert.Equal(t, incomingBody, uncompressed, "the event payload body must be forwarded byte-for-byte")

		// Authorization is the one credential header the analytics path deliberately rewrites, swapping
		// the incoming credential for the environment's anchor.
		assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"analytics upstream must carry the anchor credential, not the incoming request credential")

		// Headers the analytics forwarder legitimately changes or adds, excluded from the
		// "forwarded unchanged" comparison:
		//   Authorization             - swapped to the environment anchor (asserted separately above)
		//   X-LaunchDarkly-Payload-ID - a fresh UUID the sender stamps on each analytics payload
		//   Content-Encoding          - the sender gzip-compresses the payload
		//   Content-Length            - reflects the compressed body, set by the transport
		//   Accept-Encoding           - added by the Go HTTP transport
		//   User-Agent                - Relay's own SDK-client User-Agent, from the base headers
		allowedDelta := headerSet(
			"Authorization",
			"X-LaunchDarkly-Payload-ID",
			"Content-Encoding",
			"Content-Length",
			"Accept-Encoding",
			"User-Agent",
		)
		assertProxiedHeadersMatchExcept(t, incomingHeaders, r.Request.Header, allowedDelta)
	})
}

// TestDiagnosticUpstreamPayloadIsByteIdentical is the payload-regression form of the diagnostic routing
// test: the body must be byte-for-byte identical, and because the diagnostic path is a verbatim reverse
// proxy, every header including Authorization must be forwarded unchanged except the transport/compression
// headers the sender adds.
func TestDiagnosticUpstreamPayloadIsByteIdentical(t *testing.T) {
	const sdkAuth = "sdk-original-diagnostic-client-auth"

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		incomingBody := []byte(eventPayloadForVerbatimOnly)
		incomingHeaders := headersWithEventSchema(0)
		incomingHeaders.Set("Authorization", sdkAuth)
		incomingHeaders.Set("User-Agent", "some-sdk/1.0.0")
		// A custom header proves the diagnostic path forwards arbitrary request headers verbatim.
		incomingHeaders.Set("X-Custom-Passthrough", "passthrough-value")

		handler := p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.DiagnosticEventDataKind)
		require.NotNil(t, handler)
		w := httptest.NewRecorder()
		handler(w, st.BuildRequest("POST", "/", incomingBody, incomingHeaders))
		assert.Equal(t, 202, w.Result().StatusCode)

		r := requireUpstreamRequest(t, p.requestsCh)

		uncompressed, err := util.DecompressGzipData(r.Body)
		require.NoError(t, err)
		assert.Equal(t, incomingBody, uncompressed, "the diagnostic payload body must be forwarded byte-for-byte")

		// Unlike analytics, the diagnostic path proxies the incoming credential verbatim — the anchor is
		// never substituted.
		assert.Equal(t, sdkAuth, r.Request.Header.Get("Authorization"),
			"diagnostic upstream must proxy the incoming Authorization header verbatim")
		assert.NotEqual(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"),
			"diagnostic upstream must not use the anchor credential")

		// Only the transport/compression headers the sender adds may differ; everything else — including
		// Authorization, User-Agent, and the custom header — is forwarded unchanged.
		//   Content-Encoding - the sender gzip-compresses the payload
		//   Content-Length   - reflects the compressed body, set by the transport
		//   Accept-Encoding  - added by the Go HTTP transport
		allowedDelta := headerSet(
			"Content-Encoding",
			"Content-Length",
			"Accept-Encoding",
		)
		assertProxiedHeadersMatchExcept(t, incomingHeaders, r.Request.Header, allowedDelta)
	})
}
