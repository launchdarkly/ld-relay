package relay

// Re-anchor integration tests driven through the real RAC handler (autoConfTest), covering the
// failure and default-rotation paths that the auth-focused tests in concurrent_keys_auth_test.go do
// not: init-failure rollback (and subsequent recovery), and the backend's default-rotation array
// shape that grace-demotes the old anchor.

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/api"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// When the newly-designated anchor's SDK client fails to initialize, the re-anchor rolls back: the
// previous anchor and its siblings keep authenticating, the failed key is rejected, the environment
// stays connected, and a structured error is logged. The rollback must also leave the environment
// recoverable — a later payload rotating to a healthy key re-anchors cleanly. This drives the whole
// sequence through the RAC handler with a client factory that refuses to initialize one specific key.
func TestConcurrentKeysRAC_ReanchorInitFailureRollsBackAndRecovers(t *testing.T) {
	// The healthy recovery anchor rotated to after the failed rotation is rolled back.
	const recoveryAnchorSDKKey = config.SDKKey("sdk-recovery-anchor")

	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))

	// Fail to initialize only the first rotated anchor; every other key (the original anchor, the
	// healthy recovery anchor) builds normally and is reported on the created-clients channel.
	makeFactory := func(createdCh chan<- *testclient.FakeLDClient) sdks.ClientFactoryFunc {
		healthy := testclient.FakeLDClientFactoryWithChannel(true, createdCh)
		return func(key config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
			if key == rotatedAnchorSDKKey {
				return nil, errors.New("re-anchor: new client init refused")
			}
			return healthy(key, cfg, timeout)
		}
	}

	autoConfTestWithClientFactory(t, testAutoConfDefaultConfig, &putEvent, makeFactory, func(p autoConfTestParams) {
		anchorClient := p.awaitClient()
		assert.Equal(t, anchorSDKKey, anchorClient.Key)
		p.shouldNotCreateClient(200 * time.Millisecond)
		_ = p.awaitEnvironment(multiKeyEnvID)

		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

		// Rotate the anchor to the key whose client refuses to initialize. The synchronous re-anchor
		// build fails, so the reconcile rolls back the anchor change.
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(rotatedAnchorRep(rotatedAnchorSDKKey, 2)))

		// Wait for the rollback to settle: the structured error is logged and the failed new anchor's
		// briefly-registered mappings are torn down. The error is logged before the mapping teardown, so
		// requiring both proves we observe the fully rolled-back state (not the mid-swap registration window).
		require.Eventually(t, func() bool {
			_, err := p.relay.getEnvironment(sdkauth.New(rotatedAnchorSDKKey))
			return err != nil && p.mockLog.HasMessageMatch(ldlog.Error, "Re-anchor to SDK key .* failed")
		}, 5*time.Second, 10*time.Millisecond, "re-anchor init failure did not roll back with a structured error")

		// Previous anchor and its sibling still authenticate; the failed new anchor is rejected.
		p.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(false, rotatedAnchorSDKKey, "", "")

		// The environment is still connected — the preserved anchor's client keeps serving.
		assert.Equal(t, "connected", requireEnvStatus(t, p.relay).Status)

		// The failed rotation built nothing that stuck around.
		p.shouldNotCreateClient(200 * time.Millisecond)

		// Recovery: rotate to a healthy key. The re-anchor now commits — a new client comes up, the new
		// anchor authenticates, and the previous anchor's client is torn down.
		p.stream.Enqueue(configsource.MakeAutoConfigPatchEvent(rotatedAnchorRep(recoveryAnchorSDKKey, 3)))

		recoveryClient := p.awaitClient()
		assert.Equal(t, recoveryAnchorSDKKey, recoveryClient.Key)
		anchorClient.AwaitClose(t, 5*time.Second)
		awaitCredentialRemoved(t, p.relay, anchorSDKKey)

		p.assertSDKEndpointsAvailability(true, recoveryAnchorSDKKey, anchorMobileKey, multiKeyEnvID)
		p.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
		p.assertSDKEndpointsAvailability(false, anchorSDKKey, "", "")
		p.assertSDKEndpointsAvailability(false, rotatedAnchorSDKKey, "", "")
	})
}

// requireEnvStatus fetches the /status endpoint and returns the single environment's status rep,
// failing the test if the response does not describe exactly one environment.
func requireEnvStatus(t *testing.T, relay *Relay) api.EnvironmentStatusRep {
	t.Helper()
	req, _ := http.NewRequest("GET", "/status", nil)
	result, body := sharedtest.DoRequest(req, relay)
	require.Equal(t, http.StatusOK, result.StatusCode)
	var status api.StatusRep
	require.NoError(t, json.Unmarshal(body, &status))
	require.Len(t, status.Environments, 1)
	for _, envStatus := range status.Environments {
		return envStatus
	}
	return api.EnvironmentStatusRep{}
}

// defaultRotationRep builds the RAC EnvironmentRep the backend emits on a default rotation: sdkKey.value
// flips to newAnchor (permanent, no expiry) and the demoted old anchor stays in sdkKeys[] carrying a
// grace expiry. Mobile keys are unchanged.
func defaultRotationRep(newAnchor, demotedAnchor config.SDKKey, demotedExpiry int64, version int) envfactory.EnvironmentRep {
	return envfactory.EnvironmentRep{
		EnvID:    multiKeyEnvID,
		EnvKey:   multiKeyIdentifiers.EnvKey,
		EnvName:  multiKeyIdentifiers.EnvName,
		ProjKey:  multiKeyIdentifiers.ProjKey,
		ProjName: multiKeyIdentifiers.ProjName,
		SDKKey:   envfactory.SDKKeyRep{Value: newAnchor},
		MobKey:   anchorMobileKey,
		SDKKeys: []envfactory.ConcurrentKeyRep{
			{Key: "new-anchor-sdk", Value: string(newAnchor)},
			{Key: "old-anchor-sdk", Value: string(demotedAnchor), Expiry: msPtr(demotedExpiry)},
		},
		MobileKeys: defaultMobileKeyReps(),
		Version:    version,
	}
}

// On a default rotation the backend flips sdkKey.value to a promoted key and demotes the old anchor with
// a grace expiry, all in one patch (the array carries [new (no expiry), old (expiry)]). Relay must
// re-anchor to the new key while keeping the demoted old anchor serving through its grace window — a
// downstream stream opened on the old anchor before the rotation survives the swap — and then, once the
// grace expiry passes, drop the old anchor and disconnect that stream. The new anchor authenticates
// throughout. Uses a real (dummy) client + RAC mock so there is a live stream to observe (FakeLDClient
// never serves a stream body).
func TestConcurrentKeysRAC_DefaultRotationGraceDemotesOldAnchor(t *testing.T) {
	// The initial env has a single (anchor) SDK key, so the default-rotation array is exactly
	// [new (no expiry), old (expiry)] — the minimal shape the backend emits on a default rotation.
	putEvent := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(
		[]envfactory.ConcurrentKeyRep{{Key: "anchor-sdk", Value: string(anchorSDKKey)}},
		defaultMobileKeyReps(), 1))
	racMock := configsource.NewRACMock(t, &putEvent)

	cfg := config.Config{AutoConfig: config.AutoConfigConfig{Key: testAutoConfKey}}
	cfg.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(racMock.URL)
	// A short cleanup interval so the grace expiry is reaped promptly once it passes.
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(100 * time.Millisecond)

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

	// Connect a downstream SDK on the current anchor before the rotation, so this genuinely exercises
	// demoting the anchor out from under a live connection.
	req := sharedtest.BuildRequestWithAuth("GET", "/all", anchorSDKKey, nil)
	sharedtest.WithStreamRequest(t, req, relay, func(eventCh <-chan eventsource.Event) {
		sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)

		// Default-rotation patch: promote rotatedAnchorSDKKey to anchor (permanent), demote the old
		// anchor with a short grace expiry. The grace window is comfortably longer than the stays-open
		// check below so the survival assertion is not racing the expiry.
		grace := time.Now().Add(500 * time.Millisecond)
		racMock.Send(configsource.MakeAutoConfigPatchEvent(
			defaultRotationRep(rotatedAnchorSDKKey, anchorSDKKey, grace.UnixMilli(), 2)))

		// The swap takes effect: the promoted key becomes the environment's anchor.
		require.Eventually(t, func() bool { return env.GetAcceptedKeys().Anchor == rotatedAnchorSDKKey },
			5*time.Second, 5*time.Millisecond, "rotation did not re-anchor to the promoted key")

		// During the grace window the demoted old anchor still authenticates and its stream is undisturbed
		// by the swap (a duplicate put from the new anchor's store re-init is fine; only a close fails here).
		h.assertSDKEndpointsAvailability(true, anchorSDKKey, "", "")
		assertStreamStaysOpen(t, eventCh, 150*time.Millisecond)

		// Once the grace expiry passes, the cleanup ticker drops the old anchor and disconnects its stream.
		awaitStreamClosed(t, eventCh, 5*time.Second)
	})

	// After the grace window: the old anchor is gone; the new anchor still authenticates.
	awaitCredentialRemoved(t, relay, anchorSDKKey)
	h.assertSDKEndpointsAvailability(false, anchorSDKKey, "", "")
	h.assertSDKEndpointsAvailability(true, rotatedAnchorSDKKey, anchorMobileKey, multiKeyEnvID)

	// Sanity: the new anchor really is the one relay resolves for upstream.
	_, errNew := relay.getEnvironment(sdkauth.New(rotatedAnchorSDKKey))
	assert.NoError(t, errNew)
}
