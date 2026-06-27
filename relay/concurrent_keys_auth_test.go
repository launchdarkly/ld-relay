package relay

// Integration scenarios for multi-key (concurrent SDK keys) downstream authentication.
//
// These tests exercise the NEW behavior: an environment whose accepted set carries multiple SDK
// keys and multiple mobile keys via the sdkKeys[]/mobileKeys[] array wire format, with a single
// anchor key owning the one upstream connection. They complement — and deliberately do not
// duplicate — existing coverage:
//
//   - Single-key / legacy expiring{}-slot rotation is covered by TestAutoConfigInitWithExpiringSDKKey,
//     TestOfflineModeDeprecatedSDKKeyIsRespectedIfExpiryInFuture, TestOfflineModeSDKKeyCanExpire, etc.
//     Those assert on the credential SET via the legacy rotation path; these assert downstream
//     AUTHENTICATION (and live stream connect/disconnect) via the array path.
//   - Generic unknown-credential -> 401 is covered at the middleware/end-to-end layer; here we only
//     test the multi-key-specific rejection (a credential outside a populated accepted set, and a
//     key removed from the accepted set on reconcile).
//
// The tests reuse the in-process harnesses (autoConfTest for RAC, offlineModeTest for offline) so
// they get assertSDKEndpointsAvailability (auth accept/reject) and awaitClient/shouldNotCreateClient
// (proving the anchor owns the only upstream connection).

import (
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shared identifiers for the multi-key environment used across these scenarios. The anchor is the
// key the singular sdkKey.value/mobKey points to; the "extra" keys are additional accepted entries
// that are NOT the anchor.
const (
	ckEnvID     = c.EnvironmentID("ck-multikey-env")
	ckAnchorSDK = c.SDKKey("sdk-ck-anchor")
	ckExtraSDK  = c.SDKKey("sdk-ck-extra")
	ckAnchorMob = c.MobileKey("mob-ck-anchor")
	ckExtraMob  = c.MobileKey("mob-ck-extra")
	ckFlagKey   = "ck-flag"
)

var ckIdentifiers = relayenv.EnvIdentifiers{
	ProjName: "CK Project",
	ProjKey:  "ck-proj",
	EnvName:  "CK Env",
	EnvKey:   "ck-env",
}

// ckMultiKeyRep builds a RAC/offline EnvironmentRep for the multi-key env using the array wire
// format. The anchor is always ckAnchorSDK/ckAnchorMob (singular sdkKey/mobKey); callers pass the
// full sdkKeys[]/mobileKeys[] arrays (which must include the anchor entry).
func ckMultiKeyRep(sdkKeys, mobileKeys []envfactory.ConcurrentKeyRep, version int) envfactory.EnvironmentRep {
	return envfactory.EnvironmentRep{
		EnvID:      ckEnvID,
		EnvKey:     ckIdentifiers.EnvKey,
		EnvName:    ckIdentifiers.EnvName,
		ProjKey:    ckIdentifiers.ProjKey,
		ProjName:   ckIdentifiers.ProjName,
		SDKKey:     envfactory.SDKKeyRep{Value: ckAnchorSDK},
		MobKey:     ckAnchorMob,
		SDKKeys:    sdkKeys,
		MobileKeys: mobileKeys,
		Version:    version,
	}
}

// ckDefaultSDKKeys / ckDefaultMobileKeys are the standard two-entry arrays (anchor + one extra,
// both permanent) used by the scenarios that don't involve expiry.
func ckDefaultSDKKeys() []envfactory.ConcurrentKeyRep {
	return []envfactory.ConcurrentKeyRep{
		{Key: "anchor-sdk", Value: string(ckAnchorSDK)},
		{Key: "extra-sdk", Value: string(ckExtraSDK)},
	}
}

func ckDefaultMobileKeys() []envfactory.ConcurrentKeyRep {
	return []envfactory.ConcurrentKeyRep{
		{Key: "anchor-mob", Value: string(ckAnchorMob)},
		{Key: "extra-mob", Value: string(ckExtraMob)},
	}
}

// ckArchiveEnv builds the offline-mode ArchiveEnvironment equivalent of ckMultiKeyRep, carrying the
// accepted SDK/mobile key sets directly plus a single flag so the data store initializes.
func ckArchiveEnv(sdkKeys []envfactory.AcceptedSDKKey, mobileKeys []envfactory.AcceptedMobileKey) filedata.ArchiveEnvironment {
	return filedata.ArchiveEnvironment{
		Params: envfactory.EnvironmentParams{
			EnvID:              ckEnvID,
			SDKKey:             ckAnchorSDK,
			MobileKey:          ckAnchorMob,
			AcceptedSDKKeys:    sdkKeys,
			AcceptedMobileKeys: mobileKeys,
			Identifiers:        ckIdentifiers,
		},
		SDKData: ckSDKData(),
	}
}

func ckDefaultAcceptedSDKKeys() []envfactory.AcceptedSDKKey {
	return []envfactory.AcceptedSDKKey{{Value: ckAnchorSDK}, {Value: ckExtraSDK}}
}

func ckDefaultAcceptedMobileKeys() []envfactory.AcceptedMobileKey {
	return []envfactory.AcceptedMobileKey{{Value: ckAnchorMob}, {Value: ckExtraMob}}
}

func ckSDKData() []ldstoretypes.Collection {
	flag := ldbuilders.NewFlagBuilder(ckFlagKey).Version(1).On(true).Build()
	return []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: ckFlagKey, Item: sharedtest.FlagDesc(flag)},
			},
		},
	}
}

// --- S5: a valid non-anchor key authenticates downstream; one upstream connection serves all ---

func TestConcurrentKeysRAC_NonAnchorKeysAuthenticate(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(ckMultiKeyRep(ckDefaultSDKKeys(), ckDefaultMobileKeys(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		// The anchor opens the single upstream client; no second client for the non-anchor key.
		anchorClient := p.awaitClient()
		assert.Equal(t, ckAnchorSDK, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		_ = p.awaitEnvironment(ckEnvID)

		// Every accepted credential authenticates downstream, anchor and non-anchor alike.
		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
		p.assertSDKEndpointsAvailability(true, ckExtraSDK, ckExtraMob, "")
	})
}

func TestConcurrentKeysOffline_NonAnchorKeysAuthenticate(t *testing.T) {
	offlineModeTest(t, c.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(ckArchiveEnv(ckDefaultAcceptedSDKKeys(), ckDefaultAcceptedMobileKeys()))

		anchorClient := p.awaitClient()
		assert.Equal(t, ckAnchorSDK, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)

		env := p.awaitEnvironment(ckEnvID)

		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
		p.assertSDKEndpointsAvailability(true, ckExtraSDK, ckExtraMob, "")

		// Flag data flows through the shared store that the single anchor connection populates.
		flags, err := env.GetStore().GetAll(ldstoreimpl.Features())
		require.NoError(t, err)
		assert.NotEmpty(t, flags)
	})
}

// --- S4: per-credential rejection within a multi-key env ---

func TestConcurrentKeysRAC_RejectsCredentialsOutsideAcceptedSet(t *testing.T) {
	putEvent := configsource.MakeAutoConfigPutEvent(ckMultiKeyRep(ckDefaultSDKKeys(), ckDefaultMobileKeys(), 1))
	autoConfTest(t, testAutoConfDefaultConfig, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		_ = p.awaitEnvironment(ckEnvID)

		// (a) Accepted siblings authenticate; a credential outside the accepted set is rejected.
		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
		p.assertSDKEndpointsAvailability(true, ckExtraSDK, ckExtraMob, "")
		p.assertSDKEndpointsAvailability(false,
			c.SDKKey("sdk-not-accepted"), c.MobileKey("mob-not-accepted"), c.EnvironmentID("env-not-accepted"))

		// (b) Remove the extra keys via a patch that carries only the anchor; they must then be
		//     rejected, while the anchor (still accepted) keeps authenticating.
		anchorOnly := ckMultiKeyRep(
			[]envfactory.ConcurrentKeyRep{{Key: "anchor-sdk", Value: string(ckAnchorSDK)}},
			[]envfactory.ConcurrentKeyRep{{Key: "anchor-mob", Value: string(ckAnchorMob)}},
			2,
		)
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(anchorOnly))

		awaitCredentialRemoved(t, p.relay, ckExtraSDK)

		p.assertSDKEndpointsAvailability(false, ckExtraSDK, ckExtraMob, "")
		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
	})
}

func TestConcurrentKeysOffline_RejectsCredentialsOutsideAcceptedSet(t *testing.T) {
	offlineModeTest(t, c.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(ckArchiveEnv(ckDefaultAcceptedSDKKeys(), ckDefaultAcceptedMobileKeys()))
		_ = p.awaitClient()
		_ = p.awaitEnvironment(ckEnvID)

		p.assertSDKEndpointsAvailability(true, ckExtraSDK, ckExtraMob, "")
		p.assertSDKEndpointsAvailability(false,
			c.SDKKey("sdk-not-accepted"), c.MobileKey("mob-not-accepted"), c.EnvironmentID("env-not-accepted"))

		// Reload with only the anchor accepted; the extra keys are dropped immediately (omitted).
		p.updateHandler.UpdateEnvironment(ckArchiveEnv(
			[]envfactory.AcceptedSDKKey{{Value: ckAnchorSDK}},
			[]envfactory.AcceptedMobileKey{{Value: ckAnchorMob}},
		))

		awaitCredentialRemoved(t, p.relay, ckExtraSDK)

		p.assertSDKEndpointsAvailability(false, ckExtraSDK, ckExtraMob, "")
		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
	})
}

// --- S1: a connected SDK is disconnected when its (non-anchor) key expires ---

// The downstream SDK connects on a non-anchor key while that key is still permanent, so the
// connection establishes independent of expiry timing. We then give the connected key a near-future
// expiry; once the timestamp passes, the cleanup ticker must drop the key AND disconnect the open
// stream. Covered for both an SDK key and a mobile key. (The live open-connection teardown is
// verified on the offline path, which uses a real SDK client that actually serves stream data;
// the RAC equivalent — TestConcurrentKeysRAC_KeyExpiryRemovesCredential — verifies the same
// expiry->reject outcome at the auth layer, since FakeLDClient does not serve stream data.)
func TestConcurrentKeysOffline_ConnectedSDKDisconnectedWhenKeyExpires(t *testing.T) {
	// The server-side stream (/all) emits "put"; the mobile streams (/meval, /mping) emit "ping".
	run := func(t *testing.T, streamPath, firstEvent string, connectKey credential.SDKCredential, expiringSDK bool) {
		cfg := c.Config{}
		cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
		offlineModeTest(t, cfg, func(p offlineModeTestParams) {
			p.updateHandler.AddEnvironment(ckArchiveEnv(ckDefaultAcceptedSDKKeys(), ckDefaultAcceptedMobileKeys()))
			_ = p.awaitClient()
			env := p.awaitEnvironment(ckEnvID)
			require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

			req := sharedtest.BuildRequestWithAuth("GET", streamPath, connectKey, nil)
			sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				// Confirm the stream is live before we expire the key.
				sharedtest.AwaitEventOfType(t, eventCh, firstEvent, 5*time.Second)

				// Give the connected non-anchor key a near-future expiry; keep the anchor permanent.
				expiry := time.Now().Add(100 * time.Millisecond)
				sdkKeys := []envfactory.AcceptedSDKKey{{Value: ckAnchorSDK}, {Value: ckExtraSDK}}
				mobileKeys := []envfactory.AcceptedMobileKey{{Value: ckAnchorMob}, {Value: ckExtraMob}}
				if expiringSDK {
					sdkKeys[1].Expiry = expiry
				} else {
					mobileKeys[1].Expiry = expiry
				}
				p.updateHandler.UpdateEnvironment(ckArchiveEnv(sdkKeys, mobileKeys))

				// The cleanup ticker drops the expired key and disconnects this stream.
				awaitStreamClosed(t, eventCh, 5*time.Second)
			})

			// After expiry: the dropped key no longer authenticates; the anchor (sibling) still does.
			if expiringSDK {
				p.assertSDKEndpointsAvailability(false, ckExtraSDK, "", "")
			} else {
				p.assertSDKEndpointsAvailability(false, "", ckExtraMob, "")
			}
			p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
		})
	}

	t.Run("sdk key", func(t *testing.T) { run(t, "/all", "put", ckExtraSDK, true) })
	t.Run("mobile key", func(t *testing.T) {
		run(t, "/meval/eyJrZXkiOiJ1c2Vya2V5In0=", "ping", ckExtraMob, false)
	})
}

func TestConcurrentKeysRAC_KeyExpiryRemovesCredential(t *testing.T) {
	cfg := testAutoConfDefaultConfig
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	putEvent := configsource.MakeAutoConfigPutEvent(ckMultiKeyRep(ckDefaultSDKKeys(), ckDefaultMobileKeys(), 1))
	autoConfTest(t, cfg, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		_ = p.awaitEnvironment(ckEnvID)
		p.assertSDKEndpointsAvailability(true, ckExtraSDK, ckExtraMob, "")

		// Patch: the non-anchor keys gain a near-future expiry; the anchor stays permanent.
		expiry := time.Now().Add(100 * time.Millisecond).UnixMilli()
		patch := ckMultiKeyRep(
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-sdk", Value: string(ckAnchorSDK)},
				{Key: "extra-sdk", Value: string(ckExtraSDK), Expiry: msPtr(expiry)},
			},
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-mob", Value: string(ckAnchorMob)},
				{Key: "extra-mob", Value: string(ckExtraMob), Expiry: msPtr(expiry)},
			},
			2,
		)
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(patch))

		// Once the expiry passes, the cleanup ticker drops the keys: they stop authenticating while
		// the anchor is unaffected.
		awaitCredentialRemoved(t, p.relay, ckExtraSDK)
		p.assertSDKEndpointsAvailability(false, ckExtraSDK, ckExtraMob, "")
		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
	})
}

// --- S2: a connected SDK stays connected when its key gains a (future) expiry it hasn't reached ---

func TestConcurrentKeysOffline_ConnectionSurvivesWhenKeyGainsFutureExpiry(t *testing.T) {
	cfg := c.Config{}
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	offlineModeTest(t, cfg, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(ckArchiveEnv(ckDefaultAcceptedSDKKeys(), ckDefaultAcceptedMobileKeys()))
		_ = p.awaitClient()
		env := p.awaitEnvironment(ckEnvID)
		require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

		req := sharedtest.BuildRequestWithAuth("GET", "/all", ckExtraSDK, nil)
		sharedtest.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
			sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

			// Give the connected non-anchor key a FAR-future expiry; the cleanup ticker must not drop
			// it during the test, so the open stream stays connected.
			expiry := time.Now().Add(1 * time.Hour)
			p.updateHandler.UpdateEnvironment(ckArchiveEnv(
				[]envfactory.AcceptedSDKKey{{Value: ckAnchorSDK}, {Value: ckExtraSDK, Expiry: expiry}},
				ckDefaultAcceptedMobileKeys(),
			))

			assertStreamStaysOpen(t, eventCh, 500*time.Millisecond)
		})

		// The key is now in the deprecated-but-accepted set (its expiry was applied) and still authenticates.
		require.Eventually(t, func() bool { return credsContain(env.GetDeprecatedCredentials(), ckExtraSDK) },
			time.Second, 5*time.Millisecond, "expiry was not applied to the connected key")
		p.assertSDKEndpointsAvailability(true, ckExtraSDK, "", "")
	})
}

func TestConcurrentKeysRAC_KeyWithFutureExpiryStillAuthenticates(t *testing.T) {
	cfg := testAutoConfDefaultConfig
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)
	putEvent := configsource.MakeAutoConfigPutEvent(ckMultiKeyRep(ckDefaultSDKKeys(), ckDefaultMobileKeys(), 1))
	autoConfTest(t, cfg, &putEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()
		env := p.awaitEnvironment(ckEnvID)

		// Patch: the non-anchor keys gain a FAR-future expiry; they remain accepted (not dropped).
		expiry := time.Now().Add(1 * time.Hour).UnixMilli()
		patch := ckMultiKeyRep(
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-sdk", Value: string(ckAnchorSDK)},
				{Key: "extra-sdk", Value: string(ckExtraSDK), Expiry: msPtr(expiry)},
			},
			[]envfactory.ConcurrentKeyRep{
				{Key: "anchor-mob", Value: string(ckAnchorMob)},
				{Key: "extra-mob", Value: string(ckExtraMob), Expiry: msPtr(expiry)},
			},
			2,
		)
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(patch))

		// Confirm the expiry was applied (the key is now deprecated-but-accepted) and, after the
		// cleanup ticker has had time to run, the future-dated key still authenticates.
		require.Eventually(t, func() bool { return credsContain(env.GetDeprecatedCredentials(), ckExtraSDK) },
			time.Second, 5*time.Millisecond, "future expiry was not applied")
		p.assertSDKEndpointsAvailability(true, ckExtraSDK, ckExtraMob, "")
		p.assertSDKEndpointsAvailability(true, ckAnchorSDK, ckAnchorMob, ckEnvID)
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
	for _, cr := range creds {
		if cr == target {
			return true
		}
	}
	return false
}

// awaitCredentialRemoved blocks until the given credential no longer resolves to an environment.
func awaitCredentialRemoved(t *testing.T, relay *Relay, cred credential.SDKCredential) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := relay.getEnvironment(sdkauth.New(cred))
		return err != nil
	}, time.Second, 5*time.Millisecond, "credential was not removed from the accepted set")
}
