package relay

// Downstream authentication for environments that accept multiple SDK keys and multiple mobile keys
// (the sdkKeys[]/mobileKeys[] array wire format), where a single anchor key owns the one upstream
// connection. These are permanent regression tests for that behavior.
//
// They complement — and deliberately do not duplicate — existing coverage:
//
//   - Single-key / legacy expiring{}-slot rotation is covered by TestAutoConfigInitWithExpiringSDKKey,
//     TestOfflineModeDeprecatedSDKKeyIsRespectedIfExpiryInFuture, TestOfflineModeSDKKeyCanExpire, etc.
//     Those assert on the credential set via the legacy rotation path; these assert downstream
//     authentication (and live stream connect/disconnect) via the array path.
//   - Generic unknown-credential -> 401 is covered at the middleware/end-to-end layer; here we only
//     test rejection that is specific to a multi-key environment (a credential outside a populated
//     accepted set, and a key removed from the accepted set on reconcile).
//
// The tests reuse the in-process harnesses (autoConfTest for RAC, offlineModeTest for offline) so
// they get assertSDKEndpointsAvailability (auth accept/reject) and awaitClient/shouldNotCreateClient
// (proving the anchor owns the only upstream connection).

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/api"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shared identifiers for the multi-key environment used across these tests. The anchor is the key
// the singular sdkKey.value/mobKey points to; the "extra" keys are additional accepted entries that
// are NOT the anchor.
const (
	multiKeyEnvID   = config.EnvironmentID("multikey-env")
	anchorSDKKey    = config.SDKKey("sdk-anchor")
	extraSDKKey     = config.SDKKey("sdk-extra")
	anchorMobileKey = config.MobileKey("mob-anchor")
	extraMobileKey  = config.MobileKey("mob-extra")
	multiKeyFlagKey = "multikey-flag"

	// rotatedAnchorSDKKey is a brand-new SDK key that becomes the anchor when the anchor is rotated.
	rotatedAnchorSDKKey = config.SDKKey("sdk-rotated-anchor")

	// addedSDKKey is a brand-new non-anchor SDK key introduced by a patch that adds an array entry.
	addedSDKKey = config.SDKKey("sdk-added")
)

var multiKeyIdentifiers = relayenv.EnvIdentifiers{
	ProjName: "Multi-Key Project",
	ProjKey:  "multikey-proj",
	EnvName:  "Multi-Key Env",
	EnvKey:   "multikey-env",
}

// multiKeyEnvRep builds a RAC/offline EnvironmentRep for the multi-key env using the array wire
// format. The anchor is always anchorSDKKey/anchorMobileKey (singular sdkKey/mobKey); callers pass
// the full sdkKeys[]/mobileKeys[] arrays (which must include the anchor entry).
func multiKeyEnvRep(sdkKeys, mobileKeys []envfactory.ConcurrentKeyRep, version int) envfactory.EnvironmentRep {
	return envfactory.EnvironmentRep{
		EnvID:      multiKeyEnvID,
		EnvKey:     multiKeyIdentifiers.EnvKey,
		EnvName:    multiKeyIdentifiers.EnvName,
		ProjKey:    multiKeyIdentifiers.ProjKey,
		ProjName:   multiKeyIdentifiers.ProjName,
		SDKKey:     envfactory.SDKKeyRep{Value: anchorSDKKey},
		MobKey:     anchorMobileKey,
		SDKKeys:    sdkKeys,
		MobileKeys: mobileKeys,
		Version:    version,
	}
}

// defaultSDKKeyReps / defaultMobileKeyReps are the standard two-entry arrays (anchor + one extra,
// both permanent) used by the tests that don't involve expiry.
func defaultSDKKeyReps() []envfactory.ConcurrentKeyRep {
	return []envfactory.ConcurrentKeyRep{
		{Key: "anchor-sdk", Value: string(anchorSDKKey)},
		{Key: "extra-sdk", Value: string(extraSDKKey)},
	}
}

func defaultMobileKeyReps() []envfactory.ConcurrentKeyRep {
	return []envfactory.ConcurrentKeyRep{
		{Key: "anchor-mob", Value: string(anchorMobileKey)},
		{Key: "extra-mob", Value: string(extraMobileKey)},
	}
}

// multiKeyArchiveEnv builds the offline-mode ArchiveEnvironment equivalent of multiKeyEnvRep,
// carrying the accepted SDK/mobile key sets directly plus a single flag so the data store initializes.
func multiKeyArchiveEnv(sdkKeys []envfactory.AcceptedSDKKey, mobileKeys []envfactory.AcceptedMobileKey) filedata.ArchiveEnvironment {
	return filedata.ArchiveEnvironment{
		Params: envfactory.EnvironmentParams{
			EnvID:              multiKeyEnvID,
			SDKKey:             anchorSDKKey,
			MobileKey:          anchorMobileKey,
			AcceptedSDKKeys:    sdkKeys,
			AcceptedMobileKeys: mobileKeys,
			Identifiers:        multiKeyIdentifiers,
		},
		SDKData: multiKeySDKData(),
	}
}

func defaultAcceptedSDKKeys() []envfactory.AcceptedSDKKey {
	return []envfactory.AcceptedSDKKey{{Value: anchorSDKKey}, {Value: extraSDKKey}}
}

func defaultAcceptedMobileKeys() []envfactory.AcceptedMobileKey {
	return []envfactory.AcceptedMobileKey{{Value: anchorMobileKey}, {Value: extraMobileKey}}
}

func multiKeySDKData() []ldstoretypes.Collection {
	flag := ldbuilders.NewFlagBuilder(multiKeyFlagKey).Version(1).On(true).Build()
	return []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: multiKeyFlagKey, Item: sharedtest.FlagDesc(flag)},
			},
		},
	}
}

// rotatedAnchorRep returns the env rep after the anchor has rotated to newAnchor, keeping the
// existing non-anchor SDK key (extraSDKKey) accepted and the mobile keys unchanged.
func rotatedAnchorRep(newAnchor config.SDKKey, version int) envfactory.EnvironmentRep {
	return envfactory.EnvironmentRep{
		EnvID:    multiKeyEnvID,
		EnvKey:   multiKeyIdentifiers.EnvKey,
		EnvName:  multiKeyIdentifiers.EnvName,
		ProjKey:  multiKeyIdentifiers.ProjKey,
		ProjName: multiKeyIdentifiers.ProjName,
		SDKKey:   envfactory.SDKKeyRep{Value: newAnchor},
		MobKey:   anchorMobileKey,
		SDKKeys: []envfactory.ConcurrentKeyRep{
			{Key: "rotated-anchor-sdk", Value: string(newAnchor)},
			{Key: "extra-sdk", Value: string(extraSDKKey)},
		},
		MobileKeys: defaultMobileKeyReps(),
		Version:    version,
	}
}

// A valid non-anchor key authenticates downstream; one upstream connection serves all accepted keys.

func TestConcurrentKeysRAC_NonAnchorKeysAuthenticate(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		// The anchor opens the single upstream client; no second client for the non-anchor key.
		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		_ = p.awaitEnvironment(multiKeyEnvID)

		// Every accepted credential authenticates downstream, anchor and non-anchor alike.
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
	})
}

func TestConcurrentKeysOffline_NonAnchorKeysAuthenticate(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))

		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		env := p.awaitEnvironment(multiKeyEnvID)

		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

		// Flag data flows through the shared store that the single anchor connection populates.
		flags, err := env.GetStore().GetAll(ldstoreimpl.Features())
		require.NoError(t, err)
		assert.NotEmpty(t, flags)
	})
}

// Per-credential rejection within a multi-key environment.

func TestConcurrentKeysRAC_RejectsCredentialsOutsideAcceptedSet(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		_ = p.awaitEnvironment(multiKeyEnvID)

		// (a) Accepted siblings authenticate; a credential outside the accepted set is rejected.
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(false,
			config.SDKKey("sdk-not-accepted"), config.MobileKey("mob-not-accepted"), config.EnvironmentID("env-not-accepted"))

		// (b) Remove the extra keys via a patch that carries only the anchor; they must then be
		//     rejected, while the anchor (still accepted) keeps authenticating.
		anchorOnly := multiKeyEnvRep(
			[]envfactory.ConcurrentKeyRep{{Key: "anchor-sdk", Value: string(anchorSDKKey)}},
			[]envfactory.ConcurrentKeyRep{{Key: "anchor-mob", Value: string(anchorMobileKey)}},
			2,
		)
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(anchorOnly))

		awaitCredentialRemoved(t, p.relay, extraSDKKey)

		p.assertSDKEndpointsAvailability(false, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	})
}

func TestConcurrentKeysOffline_RejectsCredentialsOutsideAcceptedSet(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))
		_ = p.awaitClient()
		_ = p.awaitEnvironment(multiKeyEnvID)

		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(false,
			config.SDKKey("sdk-not-accepted"), config.MobileKey("mob-not-accepted"), config.EnvironmentID("env-not-accepted"))

		// Reload with only the anchor accepted; the extra keys are dropped immediately (omitted).
		p.updateHandler.UpdateEnvironment(multiKeyArchiveEnv(
			[]envfactory.AcceptedSDKKey{{Value: anchorSDKKey}},
			[]envfactory.AcceptedMobileKey{{Value: anchorMobileKey}},
		))

		awaitCredentialRemoved(t, p.relay, extraSDKKey)

		p.assertSDKEndpointsAvailability(false, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	})
}

// A connected SDK is disconnected when its (non-anchor) key expires.
//
// The downstream SDK connects on a non-anchor key while that key is still permanent, so the
// connection establishes independent of expiry timing. We then give the connected key a near-future
// expiry; once the timestamp passes, the cleanup ticker must drop the key AND disconnect the open
// stream. Covered for both an SDK key and a mobile key. (The live open-connection teardown is
// verified on the offline path, which uses a real SDK client that actually serves stream data; the
// RAC equivalent — TestConcurrentKeysRAC_KeyExpiryRemovesCredential — verifies the same expiry->reject
// outcome at the auth layer, since FakeLDClient does not serve stream data.)
func TestConcurrentKeysOffline_ConnectedSDKDisconnectedWhenKeyExpires(t *testing.T) {
	// The server-side stream (/all) emits "put"; the mobile streams (/meval, /mping) emit "ping".
	run := func(t *testing.T, streamPath, firstEvent string, connectKey credential.SDKCredential, expiringSDK bool) {
		cfg := config.Config{}
		cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
		offlineModeTest(t, cfg, func(p offlineModeTestParams) {
			p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))
			_ = p.awaitClient()
			env := p.awaitEnvironment(multiKeyEnvID)
			require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

			req := sharedtest.BuildRequestWithAuth("GET", streamPath, connectKey, nil)
			sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				// Confirm the stream is live before we expire the key.
				sharedtest.AwaitEventOfType(t, eventCh, firstEvent, 5*time.Second)

				// Give the connected non-anchor key a near-future expiry; keep the anchor permanent.
				expiry := time.Now().Add(100 * time.Millisecond)
				sdkKeys := []envfactory.AcceptedSDKKey{{Value: anchorSDKKey}, {Value: extraSDKKey}}
				mobileKeys := []envfactory.AcceptedMobileKey{{Value: anchorMobileKey}, {Value: extraMobileKey}}
				if expiringSDK {
					sdkKeys[1].Expiry = expiry
				} else {
					mobileKeys[1].Expiry = expiry
				}
				p.updateHandler.UpdateEnvironment(multiKeyArchiveEnv(sdkKeys, mobileKeys))

				// The cleanup ticker drops the expired key and disconnects this stream.
				awaitStreamClosed(t, eventCh, 5*time.Second)
			})

			// After expiry: the dropped key no longer authenticates; the anchor (sibling) still does.
			if expiringSDK {
				p.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
			} else {
				p.assertSDKEndpointsAvailability(false, "", extraMobileKey, "")
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

func TestConcurrentKeysRAC_KeyExpiryRemovesCredential(t *testing.T) {
	cfg := testAutoConfDefaultConfig
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, cfg, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		_ = p.awaitEnvironment(multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

		// Patch: the non-anchor keys gain a near-future expiry; the anchor stays permanent.
		expiry := time.Now().Add(100 * time.Millisecond).UnixMilli()
		patch := multiKeyEnvRep(
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-sdk", Value: string(anchorSDKKey)},
				{Key: "extra-sdk", Value: string(extraSDKKey), Expiry: msPtr(expiry)},
			},
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-mob", Value: string(anchorMobileKey)},
				{Key: "extra-mob", Value: string(extraMobileKey), Expiry: msPtr(expiry)},
			},
			2,
		)
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(patch))

		// Once the expiry passes, the cleanup ticker drops the keys: they stop authenticating while
		// the anchor is unaffected.
		awaitCredentialRemoved(t, p.relay, extraSDKKey)
		p.assertSDKEndpointsAvailability(false, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	})
}

// A connected SDK stays connected when its key gains a future expiry it hasn't reached yet.

func TestConcurrentKeysOffline_ConnectionSurvivesWhenKeyGainsFutureExpiry(t *testing.T) {
	cfg := config.Config{}
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	offlineModeTest(t, cfg, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))
		_ = p.awaitClient()
		env := p.awaitEnvironment(multiKeyEnvID)
		require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

		req := sharedtest.BuildRequestWithAuth("GET", "/all", extraSDKKey, nil)
		sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
			sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

			// Give the connected non-anchor key a FAR-future expiry; the cleanup ticker must not drop
			// it during the test, so the open stream stays connected.
			expiry := time.Now().Add(1 * time.Hour)
			p.updateHandler.UpdateEnvironment(multiKeyArchiveEnv(
				[]envfactory.AcceptedSDKKey{{Value: anchorSDKKey}, {Value: extraSDKKey, Expiry: expiry}},
				defaultAcceptedMobileKeys(),
			))

			assertStreamStaysOpen(t, eventCh, 500*time.Millisecond)
		})

		// The key is now in the deprecated-but-accepted set (its expiry was applied) and still authenticates.
		require.Eventually(t, func() bool { return credsContain(env.GetDeprecatedCredentials(), extraSDKKey) },
			time.Second, 5*time.Millisecond, "expiry was not applied to the connected key")
		p.assertSDKEndpointsAvailability(true, extraSDKKey, "", "")
	})
}

func TestConcurrentKeysRAC_KeyWithFutureExpiryStillAuthenticates(t *testing.T) {
	cfg := testAutoConfDefaultConfig
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, cfg, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		env := p.awaitEnvironment(multiKeyEnvID)

		// Patch: the non-anchor keys gain a FAR-future expiry; they remain accepted (not dropped).
		expiry := time.Now().Add(1 * time.Hour).UnixMilli()
		patch := multiKeyEnvRep(
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-sdk", Value: string(anchorSDKKey)},
				{Key: "extra-sdk", Value: string(extraSDKKey), Expiry: msPtr(expiry)},
			},
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-mob", Value: string(anchorMobileKey)},
				{Key: "extra-mob", Value: string(extraMobileKey), Expiry: msPtr(expiry)},
			},
			2,
		)
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(patch))

		// Confirm the expiry was applied (the key is now deprecated-but-accepted) and, after the
		// cleanup ticker has had time to run, the future-dated key still authenticates.
		require.Eventually(t, func() bool { return credsContain(env.GetDeprecatedCredentials(), extraSDKKey) },
			time.Second, 5*time.Millisecond, "future expiry was not applied")
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	})
}

// Rotating the anchor in the accepted set.
//
// Both tests run on the FakeLDClient harness, so they verify the routing/credential-level behavior
// of an anchor swap. The real-upstream store handover (avoiding an empty-store window) and
// rollback-on-init-failure robustness is covered by the re-anchor tests in internal/relayenv.

// When a new anchor arrives via RAC (sdkKey.value changes to a brand-new key), the upstream client
// swaps to the new anchor and the old anchor is dropped, while the non-anchor key stays accepted.
func TestConcurrentKeysRAC_RotatingAnchorUpdatesUpstreamClient(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, anchorSDKKey, client1.Key)
		_ = p.awaitEnvironment(multiKeyEnvID)

		// A new anchor arrives; the old anchor is rotated out, the non-anchor extra key is retained.
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(rotatedAnchorRep(rotatedAnchorSDKKey, 2)))

		// The new anchor opens the single upstream client; the old anchor's client closes.
		client2 := p.awaitClient()
		assert.Equal(t, rotatedAnchorSDKKey, client2.Key)
		client1.AwaitClose(t, 5*time.Second)

		awaitCredentialRemoved(t, p.relay, anchorSDKKey)

		// The new anchor and the retained non-anchor key authenticate; the old anchor no longer does.
		p.assertSDKEndpointsAvailability(true, rotatedAnchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, "", "")
		p.assertSDKEndpointsAvailability(false, anchorSDKKey, "", "")
	})
}

// A downstream SDK connected on a non-anchor key stays connected when the anchor is rotated out from
// under it. This uses a real (dummy) SDK client + RAC mock — rather than the FakeLDClient harness —
// because FakeLDClient never serves a put on the SSE stream, so it couldn't confirm the connection
// was actually established before rotating (and thus couldn't genuinely exercise "rotate while
// connected"). The connection-survival property holds independently of the store-handover work,
// which addresses the empty-store data window during the swap, not connection drops.
func TestConcurrentKeysRAC_NonAnchorConnectionSurvivesAnchorRotation(t *testing.T) {
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

	// Connect a downstream SDK on the non-anchor key.
	req := sharedtest.BuildRequestWithAuth("GET", "/all", extraSDKKey, nil)
	sharedtest.WithStreamRequest(t, req, relay, func(eventCh <-chan eventsource.Event) {
		// Confirm the non-anchor stream is live before rotating, so this genuinely exercises
		// "rotate while connected" rather than racing the connection setup.
		sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

		// Rotate the anchor to a brand-new key while the non-anchor stream is connected.
		racMock.Send(configsource.MakeAutoConfigPatchEvent(rotatedAnchorRep(rotatedAnchorSDKKey, 2)))

		// Wait for the rotation to take effect: the new anchor resolves, the old anchor is gone.
		require.Eventually(t, func() bool {
			_, errNew := relay.getEnvironment(sdkauth.New(rotatedAnchorSDKKey))
			_, errOld := relay.getEnvironment(sdkauth.New(anchorSDKKey))
			return errNew == nil && errOld != nil
		}, 5*time.Second, 5*time.Millisecond)

		// The non-anchor key's open stream is not disconnected by the swap (a duplicate put may
		// arrive from the new anchor's initial sync — that's fine; only a close would fail here).
		assertStreamStaysOpen(t, eventCh, 500*time.Millisecond)
	})

	// The non-anchor key still authenticates after the rotation.
	h.assertSDKEndpointsAvailability(true, extraSDKKey, "", "")
}

// multiKeyArchiveEnvWithAnchor is multiKeyArchiveEnv with a caller-chosen anchor SDK key, used to
// rotate the anchor across an archive reload. The chosen anchor must also appear in sdkKeys.
func multiKeyArchiveEnvWithAnchor(anchor config.SDKKey, sdkKeys []envfactory.AcceptedSDKKey, mobileKeys []envfactory.AcceptedMobileKey) filedata.ArchiveEnvironment {
	env := multiKeyArchiveEnv(sdkKeys, mobileKeys)
	env.Params.SDKKey = anchor
	return env
}

// Rotating the anchor via an offline archive reload.
//
// A downstream SDK connected on a non-anchor key keeps its stream when the archive reloads with the
// anchor rotated to a brand-new key: the new anchor authenticates, the old anchor stops, and the
// non-anchor sibling is undisturbed. Offline re-anchoring reuses the environment's single file-data
// client rather than swapping an upstream connection (that swap is the RAC path, exercised by
// TestConcurrentKeysRAC_RotatingAnchorUpdatesUpstreamClient), so no new client is built and the open
// connection survives. The non-anchor expiry-via-reload half of the offline reload-rotation scenario is
// covered by TestConcurrentKeysOffline_ConnectedSDKDisconnectedWhenKeyExpires.
func TestConcurrentKeysOffline_AnchorRotationViaArchiveReload(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))
		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		env := p.awaitEnvironment(multiKeyEnvID)
		require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

		// Connect a downstream SDK on the non-anchor key and confirm it is live before rotating.
		req := sharedtest.BuildRequestWithAuth("GET", "/all", extraSDKKey, nil)
		sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
			sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

			// Reload the archive with the anchor rotated to a brand-new key; the non-anchor extra SDK key
			// stays accepted and the mobile keys are unchanged.
			p.updateHandler.UpdateEnvironment(multiKeyArchiveEnvWithAnchor(
				rotatedAnchorSDKKey,
				[]envfactory.AcceptedSDKKey{{Value: rotatedAnchorSDKKey}, {Value: extraSDKKey}},
				defaultAcceptedMobileKeys(),
			))

			// The offline re-anchor reuses the single file-data client, so the non-anchor stream is not
			// torn down by the swap.
			assertStreamStaysOpen(t, eventCh, 500*time.Millisecond)
		})

		// Offline re-anchoring commits without building a replacement upstream client.
		p.shouldNotCreateClient(200 * time.Millisecond)

		awaitCredentialRemoved(t, p.relay, anchorSDKKey)

		// The rotated anchor and the retained non-anchor key authenticate; the old anchor no longer does.
		p.assertSDKEndpointsAvailability(true, rotatedAnchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, "", "")
		p.assertSDKEndpointsAvailability(false, anchorSDKKey, "", "")
	})
}

// awaitStreamClosed reads from a WithStreamRequest event channel until the stream-closed sentinel
// (a nil event) arrives, failing if the timeout elapses first. Non-nil events are ignored.
func awaitStreamClosed(t *testing.T, eventCh <-chan eventsource.Event, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case e := <-eventCh:
			if e == nil {
				return // stream closed — the disconnect we expect
			}
		case <-deadline.C:
			t.Fatalf("timed out after %s waiting for the stream to be disconnected", timeout)
			return
		}
	}
}

// assertStreamStaysOpen verifies that no stream-closed sentinel arrives within the given window.
func assertStreamStaysOpen(t *testing.T, eventCh <-chan eventsource.Event, window time.Duration) {
	t.Helper()
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	for {
		select {
		case e := <-eventCh:
			if e == nil {
				t.Fatalf("stream was unexpectedly disconnected within %s", window)
				return
			}
		case <-deadline.C:
			return // still open after the window — as expected
		}
	}
}

func msPtr(v int64) *int64 { return &v }

func credsContain(creds []credential.SDKCredential, target credential.SDKCredential) bool {
	return slices.Contains(creds, target)
}

// awaitCredentialRemoved blocks until the given credential no longer resolves to an environment.
func awaitCredentialRemoved(t *testing.T, relay *Relay, cred credential.SDKCredential) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := relay.getEnvironment(sdkauth.New(cred))
		return err != nil
	}, time.Second, 5*time.Millisecond, "credential was not removed from the accepted set")
}

// An SDK-key-only environment (no mobile key) initializes and authenticates.
//
// This is the regression lock for the escaped no-mobile-key rejection: an environment configured with
// only a server-side SDK key — MobKey/mobileKeys absent — must configure and authenticate downstream
// rather than being rejected as credential-short. Covered on both the RAC and offline paths.

// sdkOnlyEnvRep builds a RAC/offline EnvironmentRep for a server-side-only environment: a single SDK
// key in sdkKeys[] and no mobile key (neither the singular mobKey nor a mobileKeys array).
func sdkOnlyEnvRep(version int) envfactory.EnvironmentRep {
	return envfactory.EnvironmentRep{
		EnvID:    multiKeyEnvID,
		EnvKey:   multiKeyIdentifiers.EnvKey,
		EnvName:  multiKeyIdentifiers.EnvName,
		ProjKey:  multiKeyIdentifiers.ProjKey,
		ProjName: multiKeyIdentifiers.ProjName,
		SDKKey:   envfactory.SDKKeyRep{Value: anchorSDKKey},
		SDKKeys:  []envfactory.ConcurrentKeyRep{{Key: "anchor-sdk", Value: string(anchorSDKKey)}},
		Version:  version,
	}
}

// sdkOnlyArchiveEnv is the offline-mode equivalent of sdkOnlyEnvRep: an accepted SDK key set of one
// and an empty accepted mobile key set (no mobile key).
func sdkOnlyArchiveEnv() filedata.ArchiveEnvironment {
	return filedata.ArchiveEnvironment{
		Params: envfactory.EnvironmentParams{
			EnvID:              multiKeyEnvID,
			SDKKey:             anchorSDKKey,
			AcceptedSDKKeys:    []envfactory.AcceptedSDKKey{{Value: anchorSDKKey}},
			AcceptedMobileKeys: []envfactory.AcceptedMobileKey{},
			Identifiers:        multiKeyIdentifiers,
		},
		SDKData: multiKeySDKData(),
	}
}

func TestConcurrentKeysRAC_SDKOnlyEnvironmentAuthenticates(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(sdkOnlyEnvRep(1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		env := p.awaitEnvironment(multiKeyEnvID)

		// The SDK key and env ID authenticate; the environment simply has no mobile key.
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, "", multiKeyEnvID)
		assert.Empty(t, env.GetAcceptedKeys().Mobile, "a server-side-only env must have no mobile keys")
	})
}

func TestConcurrentKeysOffline_SDKOnlyEnvironmentAuthenticates(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(sdkOnlyArchiveEnv())

		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		env := p.awaitEnvironment(multiKeyEnvID)

		p.assertSDKEndpointsAvailability(true, anchorSDKKey, "", multiKeyEnvID)
		assert.Empty(t, env.GetAcceptedKeys().Mobile, "a server-side-only env must have no mobile keys")

		// Flag data flows through the store that the anchor connection populates.
		flags, err := env.GetStore().GetAll(ldstoreimpl.Features())
		require.NoError(t, err)
		assert.NotEmpty(t, flags)
	})
}

// A single patch that adds one non-anchor key and removes another, with the anchor held fixed.
//
// Because the anchor value does not change, there is no re-anchor: the sole upstream client keeps
// serving. The added entry starts routing, the removed entry stops, and a downstream stream that was
// open before the patch is left undisturbed. Uses a real (dummy) client + RAC mock so there is a live
// stream to observe (FakeLDClient never serves a stream body).
func TestConcurrentKeysRAC_ArrayPatchAddsAndRemovesNonAnchorKeys(t *testing.T) {
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

	// Hold an open downstream stream on the anchor while the non-anchor entries change around it.
	req := sharedtest.BuildRequestWithAuth("GET", "/all", anchorSDKKey, nil)
	sharedtest.WithStreamRequest(t, req, relay, func(eventCh <-chan eventsource.Event) {
		sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

		// One patch that adds a new non-anchor key (addedSDKKey) and removes the existing one
		// (extraSDKKey), keeping the anchor and both mobile keys.
		patch := multiKeyEnvRep(
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-sdk", Value: string(anchorSDKKey)},
				{Key: "added-sdk", Value: string(addedSDKKey)},
			},
			defaultMobileKeyReps(),
			2,
		)
		racMock.Send(configsource.MakeAutoConfigPatchEvent(patch))

		// The added key routes and the removed key stops routing.
		require.Eventually(t, func() bool {
			_, errAdded := relay.getEnvironment(sdkauth.New(addedSDKKey))
			_, errRemoved := relay.getEnvironment(sdkauth.New(extraSDKKey))
			return errAdded == nil && errRemoved != nil
		}, 5*time.Second, 5*time.Millisecond)

		// The anchor's open stream is undisturbed by the non-anchor add/remove.
		assertStreamStaysOpen(t, eventCh, 500*time.Millisecond)
	})

	// The anchor never changed, so no re-anchor happened and the anchor still owns the connection.
	assert.Equal(t, anchorSDKKey, env.GetAcceptedKeys().Anchor)
	h.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	h.assertSDKEndpointsAvailability(true, addedSDKKey, "", "")
	h.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
}

// A malformed offline payload preserves the previous credentials and does not reconnect.
//
// The offline handler validates the credential set with BuildAcceptedSet; a structurally malformed
// payload (here, the anchor is absent from sdkKeys[]) must be rejected without applying it: the
// previously-accepted credentials stay live and the environment is not torn down or recreated. Unlike
// the RAC path there is no live stream to reconnect, so "preserve, no reconnect" is the whole policy.
func TestConcurrentKeysOffline_MalformedPayloadPreservesCredentialsWithoutReconnect(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(multiKeyArchiveEnv(defaultAcceptedSDKKeys(), defaultAcceptedMobileKeys()))
		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		_ = p.awaitEnvironment(multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

		// Reload with a structurally malformed payload: the anchor (anchorSDKKey) is not present in the
		// accepted SDK key set. The handler must preserve the previous set rather than apply this.
		malformed := multiKeyArchiveEnv(
			[]envfactory.AcceptedSDKKey{{Value: extraSDKKey}},
			defaultAcceptedMobileKeys(),
		)
		p.updateHandler.UpdateEnvironment(malformed)

		p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "Malformed credential payload for offline environment")

		// Previous credentials preserved: the malformed set was not applied, so every key that
		// authenticated before still does — and the environment itself is intact (its env-ID endpoints
		// still resolve rather than 404). The offline path has no live stream, so there is nothing to
		// reconnect; preserving the prior accepted set is the whole policy.
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
	})
}

// Removing a key's expiry in a later payload cancels the scheduled drop.
//
// A non-anchor key given a future expiry becomes deprecated-but-accepted and is scheduled to be dropped
// when the expiry passes. If a later payload carries the same key with no expiry, the reconcile refreshes
// its metadata back to permanent: it leaves the deprecated set and the cleanup ticker never drops it.
func TestConcurrentKeysRAC_DeExpiryCancelsScheduledDrop(t *testing.T) {
	cfg := testAutoConfDefaultConfig
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, cfg, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		env := p.awaitEnvironment(multiKeyEnvID)

		// Give the non-anchor SDK key a far-future expiry: it becomes deprecated-but-accepted with a drop
		// scheduled for the expiry. (Far-future so the drop cannot fire during the test — we're proving the
		// de-expiry cancels it, not that the key survives its own deadline.)
		expiry := time.Now().Add(1 * time.Hour).UnixMilli()
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(multiKeyEnvRep(
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-sdk", Value: string(anchorSDKKey)},
				{Key: "extra-sdk", Value: string(extraSDKKey), Expiry: msPtr(expiry)},
			},
			defaultMobileKeyReps(),
			2,
		)))
		require.Eventually(t, func() bool { return credsContain(env.GetDeprecatedCredentials(), extraSDKKey) },
			time.Second, 5*time.Millisecond, "expiry was not applied — nothing to cancel")

		// A later payload carries the key with no expiry: the scheduled drop is cancelled.
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 3)))

		require.Eventually(t, func() bool { return !credsContain(env.GetDeprecatedCredentials(), extraSDKKey) },
			time.Second, 5*time.Millisecond, "de-expiry did not return the key to the permanent set")

		// The key is permanent again: its expiry is cleared, so the cleanup ticker has nothing to drop.
		info, ok := env.GetAcceptedKeys().Server[extraSDKKey]
		require.True(t, ok, "the de-expired key must still be accepted")
		assert.Nil(t, info.Expiry, "de-expiry must clear the scheduled drop (the expiry returns to permanent)")

		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	})
}

// Renaming a key's identifier (same value, new "key") disturbs no credential and updates the status.
//
// Credential identity is by value, so changing only the wire "key" identifier neither drops nor re-adds
// the credential (no new upstream client, uninterrupted authentication). The reconcile does refresh the
// stored identifier, so the status endpoint surfaces the new name in sdkKeys[]. This holds whether the
// renamed key is a non-anchor or the anchor itself: the anchor is selected by value, so renaming its
// identifier — while its value stays put — is a plain rename, not a re-anchor.
func TestConcurrentKeysRAC_RenamePreservesCredentialAndUpdatesStatusIdentifier(t *testing.T) {
	const renamedID = "sdk-renamed"
	tests := []struct {
		name       string
		renamedKey config.SDKKey
	}{
		{"non-anchor key", extraSDKKey},
		{"anchor key", anchorSDKKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
			autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
				_ = p.awaitClient()
				env := p.awaitEnvironment(multiKeyEnvID)
				p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
				p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

				// Rebuild the sdkKeys array with the target key's identifier changed. The anchor's value
				// (sdkKey.value) is left as anchorSDKKey either way, so this is a rename, not a re-anchor.
				sdkKeys := defaultSDKKeyReps()
				for i := range sdkKeys {
					if config.SDKKey(sdkKeys[i].Value) == tt.renamedKey {
						sdkKeys[i].Key = renamedID
					}
				}
				p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(multiKeyEnvRep(sdkKeys, defaultMobileKeyReps(), 2)))

				// The identifier is refreshed in place — the credential itself is untouched.
				require.Eventually(t, func() bool {
					info, ok := env.GetAcceptedKeys().Server[tt.renamedKey]
					return ok && info.Key != nil && *info.Key == renamedID
				}, time.Second, 5*time.Millisecond, "the renamed identifier was not applied")

				// No re-anchor and no credential churn: the anchor is unchanged, no new client is built,
				// and both keys keep authenticating.
				assert.Equal(t, anchorSDKKey, env.GetAcceptedKeys().Anchor)
				p.shouldNotCreateClient(200 * time.Millisecond)
				p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
				p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

				// The status endpoint reflects the new identifier for that key.
				req, _ := http.NewRequest("GET", "/status", nil)
				result, body := sharedtest.DoRequest(req, p.relay)
				require.Equal(t, http.StatusOK, result.StatusCode)
				var status api.StatusRep
				require.NoError(t, json.Unmarshal(body, &status))
				require.Len(t, status.Environments, 1)
				var envStatus api.EnvironmentStatusRep
				for _, e := range status.Environments {
					envStatus = e
				}
				renamed := findSDKKeyStatus(envStatus.SDKKeys, sdks.ObscureKey(string(tt.renamedKey)))
				require.NotNil(t, renamed, "renamed key not present in status sdkKeys[]")
				assert.Equal(t, renamedID, renamed.Key)
			})
		})
	}
}

// findSDKKeyStatus returns the sdkKeys[] entry whose obscured value matches, or nil if absent.
func findSDKKeyStatus(keys []api.KeyStatus, obscuredValue string) *api.KeyStatus {
	for i := range keys {
		if keys[i].Value == obscuredValue {
			return &keys[i]
		}
	}
	return nil
}

// A single payload that adds a key, re-anchors, and removes a key all at once.
//
// The operations apply in order add -> re-anchor -> remove, so the end state is deterministic: the new
// anchor opens the sole upstream client and the old anchor's client closes; the added key is accepted;
// the old anchor and the removed non-anchor key are gone. Runs on the FakeLDClient harness, which
// verifies the routing/credential-level outcome of the swap (the real-upstream store handover is
// exercised by the re-anchor tests in the relayenv package).
func TestConcurrentKeysRAC_MixedUpdateAddsReanchorsAndRemovesInOnePayload(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()
		assert.Equal(t, anchorSDKKey, client1.Key)
		_ = p.awaitEnvironment(multiKeyEnvID)

		// One patch: add addedSDKKey, re-anchor to rotatedAnchorSDKKey (brand-new), and drop extraSDKKey.
		mixed := envfactory.EnvironmentRep{
			EnvID:    multiKeyEnvID,
			EnvKey:   multiKeyIdentifiers.EnvKey,
			EnvName:  multiKeyIdentifiers.EnvName,
			ProjKey:  multiKeyIdentifiers.ProjKey,
			ProjName: multiKeyIdentifiers.ProjName,
			SDKKey:   envfactory.SDKKeyRep{Value: rotatedAnchorSDKKey},
			MobKey:   anchorMobileKey,
			SDKKeys: []envfactory.ConcurrentKeyRep{
				{Key: "rotated-anchor-sdk", Value: string(rotatedAnchorSDKKey)},
				{Key: "added-sdk", Value: string(addedSDKKey)},
			},
			MobileKeys: defaultMobileKeyReps(),
			Version:    2,
		}
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(mixed))

		// Re-anchor: the new anchor opens the single upstream client; the old anchor's client closes and no
		// additional client is created for the added non-anchor key.
		client2 := p.awaitClient()
		assert.Equal(t, rotatedAnchorSDKKey, client2.Key)
		client1.AwaitClose(t, 5*time.Second)
		p.shouldNotCreateClient(200 * time.Millisecond)

		awaitCredentialRemoved(t, p.relay, anchorSDKKey)
		awaitCredentialRemoved(t, p.relay, extraSDKKey)

		// End state: new anchor + added key authenticate; old anchor + removed key do not.
		p.assertSDKEndpointsAvailability(true, rotatedAnchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, addedSDKKey, "", "")
		p.assertSDKEndpointsAvailability(false, anchorSDKKey, "", "")
		p.assertSDKEndpointsAvailability(false, extraSDKKey, "", "")
	})
}
