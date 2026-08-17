package relay

// Ingestion-time rejection of credentials scoped to a view (payload filtering).
//
// A view-scoped key may only see a subset of its environment's flags. The Relay Proxy serves the whole
// environment payload and has no view support, so accepting one would silently over-deliver every flag
// in the environment to an SDK entitled to a subset. These tests pin that such a key never reaches the
// accepted set — from either source — while everything around it keeps working.
//
// They mirror the four cases in concurrent_keys_auth_test.go and reuse its harnesses and fixtures
// (multiKeyEnvRep / multiKeyArchiveEnv, assertSDKEndpointsAvailability, awaitClient, awaitStreamClosed).
// The anchor and primary mobile key are the deliberate exception: the marker is ignored on them rather
// than taking the environment down, which is what TestConcurrentKeys*_ViewScopedMarkerOnDesignatedKeys*
// covers.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/api"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// A non-anchor SDK key and a non-primary mobile key, each scoped to a view.
	viewScopedSDKKey    = config.SDKKey("sdk-view-scoped")
	viewScopedMobileKey = config.MobileKey("mob-view-scoped")

	// The wire identifiers of those two entries. Non-secret, and what the WARN names.
	viewScopedSDKID = "view-scoped-sdk"
	viewScopedMobID = "view-scoped-mob"
)

// The standard two-entry wire-rep arrays plus a third entry scoped to a view, for the RAC harness. The
// offline harness builds its accepted-key params inline, alongside the keys it revokes mid-session.

func viewScopedSDKKeyReps() []envfactory.ConcurrentKeyRep {
	return append(defaultSDKKeyReps(),
		envfactory.ConcurrentKeyRep{Key: viewScopedSDKID, Value: string(viewScopedSDKKey), HasViews: true})
}

func viewScopedMobileKeyReps() []envfactory.ConcurrentKeyRep {
	return append(defaultMobileKeyReps(),
		envfactory.ConcurrentKeyRep{Key: viewScopedMobID, Value: string(viewScopedMobileKey), HasViews: true})
}

// assertViewScopedKeysAbsentFromStatus verifies the /status sdkKeys[]/mobileKeys[] arrays do not list
// the view-scoped credentials. Those arrays are rendered straight from the accepted set, so this is the
// externally-visible proof that a filtered key carries no state anywhere in the environment.
func assertViewScopedKeysAbsentFromStatus(t *testing.T, relay *Relay) {
	t.Helper()
	req, _ := http.NewRequest("GET", "/status", nil)
	result, body := sharedtest.DoRequest(req, relay)
	require.Equal(t, http.StatusOK, result.StatusCode)

	var status api.StatusRep
	require.NoError(t, json.Unmarshal(body, &status))
	require.Len(t, status.Environments, 1)
	var envStatus api.EnvironmentStatusRep
	for _, e := range status.Environments {
		envStatus = e
	}

	assert.Nil(t, findSDKKeyStatus(envStatus.SDKKeys, sdks.ObscureKey(string(viewScopedSDKKey))),
		"a view-scoped SDK key must not appear in the status sdkKeys[] array")
	assert.Nil(t, findSDKKeyStatus(envStatus.MobileKeys, sdks.ObscureKey(string(viewScopedMobileKey))),
		"a view-scoped mobile key must not appear in the status mobileKeys[] array")

	// The keys that were accepted are still all there — the filter is surgical, not a blanket drop.
	assert.NotNil(t, findSDKKeyStatus(envStatus.SDKKeys, sdks.ObscureKey(string(anchorSDKKey))))
	assert.NotNil(t, findSDKKeyStatus(envStatus.SDKKeys, sdks.ObscureKey(string(extraSDKKey))))
	assert.NotNil(t, findSDKKeyStatus(envStatus.MobileKeys, sdks.ObscureKey(string(anchorMobileKey))))
	assert.NotNil(t, findSDKKeyStatus(envStatus.MobileKeys, sdks.ObscureKey(string(extraMobileKey))))

	// Exact counts, so a filtered key surfacing under an unexpected obscured value is still caught.
	assert.Len(t, envStatus.SDKKeys, 2)
	assert.Len(t, envStatus.MobileKeys, 2)
}

// assertViewScopedKeysRejectedWarning verifies the ingestion WARN names the environment and each
// rejected identifier. The identifier is the non-secret wire name, so it is logged unobscured — an
// operator needs it to find the key in the LaunchDarkly UI.
//
// It also pins that the WARN is emitted exactly once. The design deliberately keeps
// StreamManager.validateCredentialPayload silent — it runs on every environment of every payload — so
// logging from there would double every one of these. A count assertion is what makes that regress
// loudly instead of silently.
func assertViewScopedKeysRejectedWarning(t *testing.T, mockLog *ldlogtest.MockLog) {
	t.Helper()
	pattern := multiKeyIdentifiers.GetDisplayName() + ".*rejecting credentials scoped to a view: " +
		viewScopedSDKID + ", " + viewScopedMobID
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, pattern)

	re := regexp.MustCompile(pattern)
	matches := 0
	for _, line := range mockLog.GetOutput(ldlog.Warn) {
		if re.MatchString(line) {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "the view-scoped WARN must be logged exactly once per payload")
}

// A view-scoped key never enters the accepted set: it is rejected downstream while the anchor and the
// non-view-scoped sibling in the same environment keep working.

func TestConcurrentKeysRAC_ViewScopedKeysAreRejected(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(
		multiKeyEnvRep(viewScopedSDKKeyReps(), viewScopedMobileKeyReps(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		// The anchor still opens the single upstream client; a rejected sibling changes nothing here.
		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		env := p.awaitEnvironment(multiKeyEnvID)

		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(false, viewScopedSDKKey, viewScopedMobileKey, "")

		// Absent from the accepted set entirely. That set is the single source for /status, event
		// forwarding, and the expiry ticker's schedule, so a filtered key can reach none of them.
		accepted := env.GetAcceptedKeys()
		assert.NotContains(t, accepted.Server, viewScopedSDKKey)
		assert.NotContains(t, accepted.Mobile, viewScopedMobileKey)

		assertViewScopedKeysAbsentFromStatus(t, p.relay)
		assertViewScopedKeysRejectedWarning(t, p.mockLog)
	})
}

// A malformed payload that also carries view-scoped keys logs the malformed error and stays silent
// about the view-scoped ones.
//
// The handler discards the whole payload and preserves the previous credentials in that case, so it
// never actually rejected anything — a WARN naming keys would describe an action that did not happen.
// BuildAcceptedSet enforces this by returning an empty ViewScopedKeys on every error path; this pins
// that the handler honors it, which moving the log call above the error branch would break.
func TestConcurrentKeysOffline_MalformedPayloadSuppressesViewScopedWarning(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		// The anchor is absent from the accepted SDK keys — structurally malformed — alongside a
		// view-scoped entry that would otherwise be reported as rejected.
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(
			[]envfactory.AcceptedSDKKey{
				{Key: "other-sdk", Value: config.SDKKey("sdk-not-the-anchor")},
				{Key: viewScopedSDKID, Value: viewScopedSDKKey, HasViews: true},
			},
			[]envfactory.AcceptedMobileKey{{Key: "anchor-mob", Value: anchorMobileKey}},
		))

		_ = p.awaitEnvironment(multiKeyEnvID)

		p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "Malformed credential payload for offline environment")
		p.mockLog.AssertMessageMatch(t, false, ldlog.Warn, "rejecting credentials scoped to a view")
	})
}

// A key that gains a view mid-session is revoked on the next payload, and a connected SDK using it is
// disconnected.
//
// This needs no new production code: reconcileAcceptedKeys revokes any key absent from the desired set
// immediately rather than on an expiry timestamp, and RemoveConnectionMapping unmaps before the streams
// are torn down so a reconnect is rejected. These tests assert that behavior rather than build it. The
// live teardown is verified on the offline path, which uses a real SDK client that actually serves
// stream data (mirroring
// TestConcurrentKeysOffline_SiblingStreamSurvivesWhileExpiringKeyDisconnects).
//
// The initial AddEnvironment payload also carries an already-view-scoped SDK key and mobile key, so this
// covers the offline *add* call site's ingestion filter (never accepted, absent from /status, WARN
// emitted exactly once) alongside the offline *update* call site exercised below.
func TestConcurrentKeysOffline_ConnectedSDKDisconnectedWhenKeyGainsView(t *testing.T) {
	// The server-side stream (/all) emits "put"; the mobile streams (/meval, /mping) emit "ping".
	run := func(t *testing.T, streamPath, firstEvent string, connectKey credential.SDKCredential, viewOnSDK bool) {
		// Named entries, unlike the shared default* fixtures — the WARN interpolates the wire
		// identifier, so this is also what lets the log assertion below name a specific key.
		const (
			revokedSDKID = "extra-sdk"
			revokedMobID = "extra-mob"
		)
		named := func(viewScoped bool) ([]envfactory.AcceptedSDKKey, []envfactory.AcceptedMobileKey) {
			sdkKeys := []envfactory.AcceptedSDKKey{
				{Key: "anchor-sdk", Value: anchorSDKKey},
				{Key: revokedSDKID, Value: extraSDKKey},
			}
			mobileKeys := []envfactory.AcceptedMobileKey{
				{Key: "anchor-mob", Value: anchorMobileKey},
				{Key: revokedMobID, Value: extraMobileKey},
			}
			if viewOnSDK {
				sdkKeys[1].HasViews = viewScoped
			} else {
				mobileKeys[1].HasViews = viewScoped
			}
			return sdkKeys, mobileKeys
		}

		offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
			// The add payload carries the two named non-anchor keys plus one already-view-scoped SDK key
			// and mobile key, so the add handler's ingestion filter is exercised here too.
			initialSDK, initialMob := named(false)
			p.updateHandler.AddEnvironment(multiKeyArchiveEnv(
				append(initialSDK, envfactory.AcceptedSDKKey{Key: viewScopedSDKID, Value: viewScopedSDKKey, HasViews: true}),
				append(initialMob, envfactory.AcceptedMobileKey{Key: viewScopedMobID, Value: viewScopedMobileKey, HasViews: true}),
			))
			anchorClient := p.awaitClient()
			assert.Equal(t, anchorSDKKey, anchorClient.Key)
			p.shouldNotCreateClient(200 * time.Millisecond)
			env := p.awaitEnvironment(multiKeyEnvID)
			require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

			// The add handler filtered both view-scoped entries: neither is accepted, neither reaches
			// /status, and the WARN naming them is emitted exactly once.
			p.assertSDKEndpointsAvailability(false, viewScopedSDKKey, viewScopedMobileKey, "")
			addAccepted := env.GetAcceptedKeys()
			assert.NotContains(t, addAccepted.Server, viewScopedSDKKey)
			assert.NotContains(t, addAccepted.Mobile, viewScopedMobileKey)
			assertViewScopedKeysAbsentFromStatus(t, p.relay)
			assertViewScopedKeysRejectedWarning(t, p.mockLog)

			req := sharedtest.BuildRequestWithAuth("GET", streamPath, connectKey, nil)
			sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				// Confirm the stream is live before the key gains a view.
				sharedtest.AwaitEventOfType(t, eventCh, firstEvent, 5*time.Second)

				// Reload the archive with the connected non-anchor key now scoped to a view, keeping
				// the anchor and primary untouched.
				p.updateHandler.UpdateEnvironment(multiKeyArchiveEnv(named(true)))

				// Revocation is immediate on reconcile — no expiry timestamp and no cleanup ticker.
				awaitStreamClosed(t, eventCh, 5*time.Second)
			})

			// The revoked key no longer authenticates; the anchor and primary are undisturbed.
			revokedID := revokedSDKID
			if viewOnSDK {
				p.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
			} else {
				revokedID = revokedMobID
				p.assertSDKEndpointsAvailability(false, "", extraMobileKey, "")
			}
			p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)

			// The offline update handler logs the rejection too, not just the add handler.
			p.mockLog.AssertMessageMatch(t, true, ldlog.Warn,
				multiKeyIdentifiers.GetDisplayName()+".*rejecting credentials scoped to a view: "+revokedID)

			// Losing the view re-admits the key: the filter is stateless, not a one-way latch. This is
			// the remediation an operator performs after seeing the WARN, so it has to work.
			p.updateHandler.UpdateEnvironment(multiKeyArchiveEnv(named(false)))
			if viewOnSDK {
				p.assertSDKEndpointsAvailability(true, extraSDKKey, "", "")
			} else {
				p.assertSDKEndpointsAvailability(true, "", extraMobileKey, "")
			}
			p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		})
	}

	t.Run("sdk key", func(t *testing.T) { run(t, "/all", "put", extraSDKKey, true) })
	t.Run("mobile key", func(t *testing.T) {
		// base64 of {"key":"userkey","kind":"user"} — a valid context, not the legacy user format.
		run(t, "/meval/eyJrZXkiOiJ1c2Vya2V5Iiwia2luZCI6InVzZXIifQ==", "ping", extraMobileKey, false)
	})
}

// The RAC update call site's equivalent — a non-anchor key that gains a view stops routing, and the WARN
// names it — is folded into TestConcurrentKeysRAC_RejectsCredentialsOutsideAcceptedSet, whose removal
// patch marks its non-anchor entries view-scoped. That an open stream on a *different* credential
// survives the revocation alongside it is owned by
// TestConcurrentKeysRAC_NonAnchorConnectionSurvivesAnchorRotation: removeCredential cannot tell which
// credential a given open stream belongs to, so both traverse the same teardown path.
