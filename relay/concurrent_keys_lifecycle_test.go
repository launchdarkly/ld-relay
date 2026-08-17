package relay

// Key-lifecycle integration tests that observe live downstream SSE streams (real SDK clients via the
// offline harness, or a dummy client + RAC mock) rather than just the auth layer: mixed reconcile
// updates, revocation by omission, and sibling-stream continuity during a targeted disconnect.

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

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

// A non-anchor key omitted from the next RAC patch is revoked immediately (not on a grace timer), and
// a downstream SDK connected on that key is disconnected as part of the revocation. The anchor, which
// the patch retains, keeps authenticating. Uses a real (dummy) client + RAC mock so there is a live
// stream to observe being torn down.
func TestConcurrentKeysRAC_ConnectedStreamClosedWhenKeyRevokedByOmission(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	racMock := configsource.NewRACMock(t, &putEvent)

	cfg := config.Config{AutoConfig: config.AutoConfigConfig{Key: testAutoConfKey}}
	cfg.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(racMock.URL)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	relay, err := newRelayInternal(cfg, relayInternalOptions{
		loggers:       mockLog.Loggers,
		clientFactory: testclient.CreateDummyClient,
	})
	require.NoError(t, err)
	defer relay.Close()

	h := relayTestHelper{t: t, relay: relay}
	env := h.awaitEnvironment(multiKeyEnvID)
	require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

	// Connect a downstream SDK on the non-anchor key that the next patch will omit.
	req := sharedtest.BuildRequestWithAuth("GET", "/all", extraSDKKey, nil)
	sharedtest.WithStreamRequest(t, req, relay, func(eventCh <-chan eventsource.Event) {
		sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

		// Revoke by omission: a patch that carries only the anchor SDK key (extraSDKKey dropped), keeping
		// the mobile keys. The reconcile revokes the omitted key now rather than on a grace timer.
		racMock.Send(configsource.MakeAutoConfigPatchEvent(multiKeyEnvRep(
			[]envfactory.ConcurrentKeyRep{{Key: "anchor-sdk", Value: string(anchorSDKKey)}},
			defaultMobileKeyReps(),
			2,
		)))

		// The revoked key's open stream is disconnected.
		awaitStreamClosed(t, eventCh, 5*time.Second)
	})

	awaitCredentialRemoved(t, relay, extraSDKKey)
	h.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
	h.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
}

// The offline-reload twin of the case above is
// TestConcurrentKeysOffline_ConnectedSDKDisconnectedWhenKeyGainsView: "gains a view" and "omitted from
// the array" produce the identical desired-set diff inside BuildAcceptedSet — the key is simply absent —
// so both drive the same immediate-revocation-and-stream-teardown path through the offline update
// handler, and that test covers it for a mobile key as well as an SDK key.

// When one key expires, the cleanup ticker must drop it and disconnect its downstream SDKs — and only
// its own: a stream held on the anchor stays connected throughout the expiry window while a
// concurrently-open stream on the expiring non-anchor key is torn down. Uses the offline harness (real
// client that serves stream data) with two simultaneous downstream connections on the same environment.
//
// Run for both an SDK key and a mobile key, because the two kinds of downstream stream are torn down by
// different StreamProviders.
func TestConcurrentKeysOffline_SiblingStreamSurvivesWhileExpiringKeyDisconnects(t *testing.T) {
	// The server-side stream (/all) emits "put"; the mobile streams (/meval, /mping) emit "ping".
	run := func(t *testing.T, streamPath, firstEvent string, connectKey credential.SDKCredential, expiringSDK bool) {
		cfg := config.Config{}
		cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
		offlineModeTest(t, cfg, func(p offlineModeTestParams) {
			p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))
			_ = p.awaitClient()
			env := p.awaitEnvironment(multiKeyEnvID)
			require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

			// Open a stream on the anchor — the sibling that must stay connected.
			anchorReq := sharedtest.BuildRequestWithAuth("GET", "/all", anchorSDKKey, nil)
			sharedtest.WithStreamRequest(t, anchorReq, p.relay, func(anchorCh <-chan eventsource.Event) {
				sharedtest.AwaitEventOfType(t, anchorCh, "put", 5*time.Second)

				// Concurrently open a second stream on the non-anchor key that we will expire. The
				// connection is established while that key is still permanent, so it does not race the
				// expiry timing.
				expiringReq := sharedtest.BuildRequestWithAuth("GET", streamPath, connectKey, nil)
				sharedtest.WithStreamRequest(t, expiringReq, p.relay, func(expiringCh <-chan eventsource.Event) {
					sharedtest.AwaitEventOfType(t, expiringCh, firstEvent, 5*time.Second)

					// Give the connected non-anchor key a near-future expiry; the anchor and primary
					// mobile key stay permanent.
					expiry := time.Now().Add(100 * time.Millisecond)
					sdkKeys := defaultAcceptedSDKKeys()
					mobileKeys := defaultAcceptedMobileKeys()
					if expiringSDK {
						sdkKeys[1].Expiry = expiry
					} else {
						mobileKeys[1].Expiry = expiry
					}
					p.updateHandler.UpdateEnvironment(multiKeyArchiveEnv(sdkKeys, mobileKeys))

					// Across the expiry window the anchor sibling's stream stays open (the expiring key's
					// stream is being torn down on its own channel during this same window)...
					assertStreamStaysOpen(t, anchorCh, 300*time.Millisecond)
					// ...and the expiring key's stream is confirmed disconnected.
					awaitStreamClosed(t, expiringCh, 5*time.Second)
				})
			})

			// After the expiry: the dropped key no longer authenticates; the anchor sibling still does.
			if expiringSDK {
				p.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
			} else {
				p.assertSDKEndpointsAvailability(false, "", extraMobileKey, "")
			}
			p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		})
	}

	t.Run("sdk key", func(t *testing.T) { run(t, "/all", "put", extraSDKKey, true) })
	t.Run("mobile key", func(t *testing.T) { run(t, mobileEvalContextPath, "ping", extraMobileKey, false) })
}
