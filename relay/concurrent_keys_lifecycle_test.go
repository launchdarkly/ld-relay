package relay

// Key-lifecycle integration tests that observe live downstream SSE streams (real SDK clients via the
// offline harness, or a dummy client + RAC mock) rather than just the auth layer: mixed reconcile
// updates, revocation by omission, and sibling-stream continuity during a targeted disconnect.

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/eventsource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// base64 of {"key":"userkey","kind":"user"} — a valid mobile-eval context path.
const mobileEvalContextPath = "/meval/eyJrZXkiOiJ1c2Vya2V5Iiwia2luZCI6InVzZXIifQ=="

// reanchoredArchiveEnv builds an offline ArchiveEnvironment whose anchor is newAnchor and whose
// accepted SDK key set is exactly sdkKeys, keeping the standard mobile keys. Used to model an offline
// reload that re-anchors and rewrites the server-key set in one step.
func reanchoredArchiveEnv(newAnchor config.SDKKey, sdkKeys []envfactory.AcceptedSDKKey) filedata.ArchiveEnvironment {
	return filedata.ArchiveEnvironment{
		Params: envfactory.EnvironmentParams{
			EnvID:              multiKeyEnvID,
			SDKKey:             newAnchor,
			MobileKey:          anchorMobileKey,
			AcceptedSDKKeys:    sdkKeys,
			AcceptedMobileKeys: defaultAcceptedMobileKeys(),
			Identifiers:        multiKeyIdentifiers,
		},
		SDKData: multiKeySDKData(),
	}
}

// A single offline reload that adds a key, re-anchors to a brand-new key, and removes the old extra
// key all at once. The end state is deterministic (add -> re-anchor -> remove): the added key, the new
// anchor, and the retained mobile keys authenticate; the old anchor and the removed key do not. In
// offline mode the re-anchor builds no new upstream client (the single file-data client keeps serving),
// and a downstream stream open on a retained credential survives the reload undisturbed.
func TestConcurrentKeysOffline_MixedUpdateAddsReanchorsAndRemovesInOneReload(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))

		initialClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, initialClient.Key)
		env := p.awaitEnvironment(multiKeyEnvID)
		require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

		// Baseline: anchor, extra SDK key, and both mobile keys authenticate.
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

		// Hold a downstream stream on a retained credential (a mobile key, unaffected by the SDK
		// re-anchor) across the reload.
		req := sharedtest.BuildRequestWithAuth("GET", mobileEvalContextPath, anchorMobileKey, nil)
		sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
			sharedtest.AwaitEventOfType(t, eventCh, "ping", 5*time.Second)

			// One reload doing three things at once: add addedSDKKey, re-anchor to the brand-new
			// rotatedAnchorSDKKey, and drop the old anchor and extraSDKKey (omitted from the new set).
			p.updateHandler.UpdateEnvironment(reanchoredArchiveEnv(rotatedAnchorSDKKey,
				[]envfactory.AcceptedSDKKey{{Value: rotatedAnchorSDKKey}, {Value: addedSDKKey}}))

			// The retained mobile-key stream is not disconnected by the reload.
			assertStreamStaysOpen(t, eventCh, 300*time.Millisecond)
		})

		// Offline re-anchor stands up no new upstream client.
		p.shouldNotCreateClient(200 * time.Millisecond)

		// End state: old anchor and removed extra key are gone; the new anchor, the added key, and the
		// retained mobile keys authenticate.
		awaitCredentialRemoved(t, p.relay, anchorSDKKey)
		awaitCredentialRemoved(t, p.relay, extraSDKKey)
		p.assertSDKEndpointsAvailability(true, rotatedAnchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, addedSDKKey, "", "")
		p.assertSDKEndpointsAvailability(true, "", extraMobileKey, "")
		p.assertSDKEndpointsAvailability(false, anchorSDKKey, "", "")
		p.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
	})
}
