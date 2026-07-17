package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoConfigKeyRotationClosesOldClientAndDoesNotDuplicateUpdates is an end-to-end regression
// test for the double-broadcast bug: after an auto-config key rotation demoted the old anchor with
// a grace-period expiry, the old anchor's SDK client kept its upstream stream open alongside the
// new anchor's client. Both clients fed the same shared store wrapper, so every upstream flag
// update was broadcast twice to connected downstream clients until the old key finally expired.
//
// Unlike the other auto-config tests, this one uses REAL SDK clients against a fake LaunchDarkly
// streaming service, so it exercises the actual upstream connections: the rotation must open a new
// upstream connection authenticated with the new key, close the demoted key's connection once the
// new anchor commits, and keep the downstream connection (authenticated with the old key) serving
// exactly one copy of each update.
func TestAutoConfigKeyRotationClosesOldClientAndDoesNotDuplicateUpdates(t *testing.T) {
	oldKey := testAutoConfEnv1.SDKKey()
	flagKey := "rotation-test-flag"
	flagV1 := ldbuilders.NewFlagBuilder(flagKey).Version(1).On(false).Build()
	flagV2 := ldbuilders.NewFlagBuilder(flagKey).Version(2).On(true).Build()

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	// The fake LD streaming service serves /all; both the original and the rotated SDK clients
	// connect to it, and sdkStream.Enqueue pushes an event to every connected client.
	putEvent := ldservices.NewServerSDKData().Flags(&flagV1).ToPutEvent()
	sdkStreamHandler, sdkStream := ldservices.ServerSideStreamingServiceHandler(putEvent)
	defer sdkStream.Close()
	recordedSDKStreamHandler, sdkRequestsCh := httphelpers.RecordingHandler(sdkStreamHandler)

	initialACEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	acHandler, acStream := httphelpers.SSEHandler(&initialACEvent)
	defer acStream.Close()

	// One upstream server plays both roles: the auto-config stream and the SDK streaming service.
	// (The auto-config path literal matches autoConfigStreamPath in internal/autoconfig, which is
	// not exported.)
	upstreamHandler := httphelpers.HandlerForPath("/relay_auto_config", acHandler, recordedSDKStreamHandler)

	eventsHandler := httphelpers.HandlerWithStatus(202)

	httphelpers.WithServer(upstreamHandler, func(upstreamServer *httptest.Server) {
		httphelpers.WithServer(eventsHandler, func(eventsServer *httptest.Server) {
			config := testAutoConfDefaultConfig
			config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(upstreamServer.URL)
			config.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(eventsServer.URL)

			// A nil clientFactory means real SDK clients.
			relay, err := newRelayInternal(config, relayInternalOptions{loggers: mockLog.Loggers})
			require.NoError(t, err)
			defer relay.Close()

			helper := relayTestHelper{t: t, relay: relay}
			helper.awaitEnvironment(testAutoConfEnv1.id)

			// The original anchor's client connects upstream with the original key.
			initialStreamReq := helpers.RequireValue(t, sdkRequestsCh, time.Second*5)
			assert.Equal(t, string(oldKey), initialStreamReq.Request.Header.Get("Authorization"))

			// awaitEnvironment only waits for the environment to be registered; the real SDK client is
			// built asynchronously (startSDKClient runs in a goroutine), so wait for it to finish
			// initializing before issuing the downstream request below. Otherwise the request can race the
			// client install and get a 503 "client was not initialized". This mirrors waitForSuccessfulInit
			// in relay_end_to_end_test.go, and the "Closing LaunchDarkly client" wait later in this test.
			require.Eventually(t, func() bool {
				return mockLog.HasMessageMatch(ldlog.Info, "Initialized LaunchDarkly client for")
			}, time.Second*2, time.Millisecond*20, "the environment's SDK client did not finish initializing")

			httphelpers.WithServer(relay, func(relayServer *httptest.Server) {
				// Connect a downstream server-side SDK client authenticated with the ORIGINAL key. It
				// stays connected across the rotation, since the old key remains valid for its grace period.
				req, err := http.NewRequest("GET", relayServer.URL+"/all", nil)
				require.NoError(t, err)
				req.Header.Set("Authorization", string(oldKey))
				stream, err := eventsource.SubscribeWithRequestAndOptions(req,
					eventsource.StreamOptionLogger(mockLog.Loggers.ForLevel(ldlog.Info)))
				require.NoError(t, err)
				defer stream.Close()

				initialPut := helpers.RequireValue(t, stream.Events, time.Second*5, "timed out waiting for initial put")
				require.Equal(t, "put", initialPut.Event())

				// Rotate: a new key becomes the anchor and the old key is demoted with a one-hour expiry
				// (the backend's default-rotation shape).
				modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
				modified.sdkKey.Expiring = envfactory.ExpiringKeyRep{
					Value:     oldKey,
					Timestamp: ldtime.UnixMillisNow() + ldtime.UnixMillisecondTime(time.Hour.Milliseconds()),
				}
				acStream.Enqueue(makeAutoConfPatchEvent(modified))

				// The new anchor's client connects upstream with the new key and re-initializes the
				// handed-over store, which republishes a put to connected downstream clients.
				rotationStreamReq := helpers.RequireValue(t, sdkRequestsCh, time.Second*5)
				assert.Equal(t, string(modified.SDKKey()), rotationStreamReq.Request.Header.Get("Authorization"))
				rotationPut := helpers.RequireValue(t, stream.Events, time.Second*5, "timed out waiting for rotation put")
				require.Equal(t, "put", rotationPut.Event())

				// The demoted key's client must be torn down once the new anchor commits; before the fix it
				// stayed connected until the key's expiry, double-broadcasting every update in the meantime.
				require.Eventually(t, func() bool {
					return mockLog.HasMessageMatch(ldlog.Info, "Closing LaunchDarkly client")
				}, time.Second*2, time.Millisecond*20, "the demoted key's SDK client was not closed after the rotation")

				// Push one flag update upstream. Exactly one upstream client (the new anchor's) receives
				// it, so connected downstream clients must receive exactly one patch.
				flagV2JSON, err := json.Marshal(flagV2)
				require.NoError(t, err)
				sdkStream.Enqueue(httphelpers.SSEEvent{
					Event: "patch",
					Data:  fmt.Sprintf(`{"path": "/flags/%s", "data": %s}`, flagKey, flagV2JSON),
				})

				patch := helpers.RequireValue(t, stream.Events, time.Second*5, "timed out waiting for patch")
				assert.Equal(t, "patch", patch.Event())
				assert.Contains(t, patch.Data(), flagKey)
				if !helpers.AssertNoMoreValues(t, stream.Events, time.Millisecond*500,
					"received a duplicate stream update after key rotation") {
					t.FailNow()
				}
			})
		})
	})
}
