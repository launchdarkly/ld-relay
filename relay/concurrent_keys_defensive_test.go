package relay

// Defensive-behavior integration tests for the RAC config stream: a malformed credential payload must
// preserve the previous accepted set and force a stream restart, and the fresh state the backend
// serves on the reconnection must then be applied. This is the relay-level twin of the unit-level
// reconnect coverage in internal/autoconfig; it verifies the whole loop end-to-end through Relay's
// downstream auth surface.

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/require"
)

// A malformed patch (anchor absent from sdkKeys[]) is rejected without being applied: the previously
// accepted credentials keep authenticating and no new key leaks in. The rejection forces the config
// stream to restart, and on the reconnection the backend serves a corrected put whose new key then
// authenticates — completing the preserve-then-recover loop from the design's malformed-payload policy.
func TestConcurrentKeysRAC_MalformedPayloadRecoversAfterReconnect(t *testing.T) {
	firstPut := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(defaultSDKKeyReps(), defaultMobileKeyReps(), 1))

	// The corrected state the backend serves on the reconnection: the original two keys plus a new one.
	correctedPut := configsource.MakeAutoConfigPutEvent(multiKeyEnvRep(
		[]envfactory.ConcurrentKeyRep{
			{Key: "anchor-sdk", Value: string(anchorSDKKey)},
			{Key: "extra-sdk", Value: string(extraSDKKey)},
			{Key: "added-sdk", Value: string(addedSDKKey)},
		},
		defaultMobileKeyReps(),
		3,
	))

	racMock := configsource.NewRACMockWithReconnect(t, &firstPut, &correctedPut)

	cfg := config.Config{AutoConfig: config.AutoConfigConfig{Key: testAutoConfKey}}
	cfg.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(racMock.URL)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	relay, err := newRelayInternal(cfg, relayInternalOptions{
		loggers:       mockLog.Loggers,
		clientFactory: testclient.FakeLDClientFactory(true),
	})
	require.NoError(t, err)
	defer relay.Close()

	h := relayTestHelper{t: t, relay: relay}
	env := h.awaitEnvironment(multiKeyEnvID)
	require.Eventually(t, func() bool { return env.GetClient() != nil }, 5*time.Second, 5*time.Millisecond)

	// Baseline: the two original keys authenticate; the corrected-put key is not accepted yet.
	h.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	h.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
	h.assertSDKEndpointsAvailability(false, addedSDKKey, "", "")

	// Send a structurally malformed patch: the anchor (anchorSDKKey) is absent from sdkKeys[]. Building
	// the raw rep this way makes credential-set validation fail at the stream parse boundary.
	malformed := multiKeyEnvRep(
		[]envfactory.ConcurrentKeyRep{{Key: "extra-sdk", Value: string(extraSDKKey)}},
		defaultMobileKeyReps(),
		2,
	)
	racMock.Send(configsource.MakeAutoConfigPatchEvent(malformed))

	// The malformed payload is rejected and logged; the previous accepted set is preserved (unchanged).
	require.Eventually(t, func() bool {
		return mockLog.HasMessageMatch(ldlog.Error, "[Mm]alformed credential payload")
	}, 5*time.Second, 10*time.Millisecond, "malformed payload was not rejected with a structured error")
	h.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	h.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")

	// Recovery: the rejection restarted the stream, and on the reconnection the backend's corrected put
	// applies — the new key now authenticates.
	require.Eventually(t, func() bool {
		_, err := relay.getEnvironment(sdkauth.New(addedSDKKey))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "corrected put was not applied after the reconnect")

	h.assertSDKEndpointsAvailability(true, anchorSDKKey, anchorMobileKey, multiKeyEnvID)
	h.assertSDKEndpointsAvailability(true, extraSDKKey, extraMobileKey, "")
	h.assertSDKEndpointsAvailability(true, addedSDKKey, "", "")
}
