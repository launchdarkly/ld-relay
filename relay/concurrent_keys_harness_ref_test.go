package relay

// TestConcurrentKeysHarnessReference is the reference integration test for the concurrent-keys
// test helpers. It demonstrates the reusable helpers added to internal/sharedtest working together
// end-to-end:
//
//   - configsource.ArchiveFixtureBuilder (offline-mode archive with flag data)
//   - configsource.RACMock (RAC SSE server delivering environment configuration)
//   - sharedtest.WithStreamRequest + sharedtest.AwaitEventOfType (consuming Relay's SSE stream)
//
// Feature-specific scenario tests live alongside the code they exercise and reuse these helpers.

import (
	"net/http"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/configsource"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	harnessEnvID     = c.EnvironmentID("harness-ref-env-id")
	harnessSDKKey    = c.SDKKey("sdk-harness-ref-key-001")
	harnessMobileKey = c.MobileKey("mob-harness-ref-key-001")
	harnessProjKey   = "ref-proj"
	harnessFlagKey   = "harness-simple-flag"
)

var harnessEnvRep = envfactory.EnvironmentRep{
	EnvID:    harnessEnvID,
	EnvKey:   "harness-ref",
	EnvName:  "Harness Reference",
	ProjKey:  harnessProjKey,
	ProjName: "Reference Project",
	MobKey:   harnessMobileKey,
	SDKKey:   envfactory.SDKKeyRep{Value: harnessSDKKey},
	Version:  1,
}

// TestConcurrentKeysHarnessReference exercises the reusable concurrent-keys test helpers.
func TestConcurrentKeysHarnessReference(t *testing.T) {
	t.Run("archive fixture + SDK stream: flag data flows through Relay's SSE stream", func(t *testing.T) {
		// 1. Build an offline-mode archive containing a single env with a simple boolean flag.
		archivePath := configsource.NewArchiveFixtureBuilder().
			AddEnv(configsource.ArchiveEnvSpec{
				Rep:    harnessEnvRep,
				DataID: "data-v1",
				Flags: map[string]any{
					harnessFlagKey: ldbuilders.NewFlagBuilder(harnessFlagKey).Version(1).On(true).Build(),
				},
			}).
			WriteTempFile(t)

		// 2. Start Relay in offline mode with the real archive manager so the flag data actually
		//    flows through the data store and into the SSE stream.
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		clientsCreatedCh := make(chan testclient.CapturedLDClient, 4)
		cfg := c.Config{}
		cfg.OfflineMode.FileDataSource = archivePath

		relay, err := newRelayInternal(cfg, relayInternalOptions{
			loggers:       mockLog.Loggers,
			clientFactory: testclient.RealLDClientFactoryWithChannel(true, clientsCreatedCh),
			// archiveManagerFactory left nil → uses the real filedata.NewArchiveManager
		})
		require.NoError(t, err)
		defer relay.Close()

		// In offline mode the archive manager loads synchronously, so the client should already
		// be in the channel; draining it confirms the environment is ready.
		_ = helpers.RequireValue(t, clientsCreatedCh, 3*time.Second, "timed out waiting for SDK client creation")

		// 3. Connect to Relay's server-side SSE stream and verify the initial put event arrives
		//    and contains the flag. WithStreamRequest drives Relay's handler in-process and cancels
		//    the request when the action returns, so there is no server/connection teardown to order.
		req := sharedtest.BuildRequestWithAuth(http.MethodGet, "/all", harnessSDKKey, nil)
		sharedtest.WithStreamRequest(t, req, relay, func(eventCh <-chan eventsource.Event) {
			event := sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)
			require.NotNil(t, event)
			assert.Contains(t, event.Data(), harnessFlagKey,
				"expected put event data to contain the flag key")
		})
	})

	t.Run("RAC mock + SDK stream: Relay discovers env from RAC and serves the SSE stream", func(t *testing.T) {
		// 1. Create a RAC mock pre-loaded with a put event for the test environment.
		putEvent := configsource.MakeAutoConfigPutEvent(harnessEnvRep)
		racMock := configsource.NewRACMock(t, &putEvent)

		// 2. Start Relay configured to use the RAC mock as its config stream. Use CreateDummyClient
		//    (rather than FakeLDClientFactory) so the data store is initialized with flag data —
		//    required for Relay to emit a put event on the SSE stream when a client connects.
		cfg := c.Config{AutoConfig: c.AutoConfigConfig{Key: testAutoConfKey}}
		cfg.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(racMock.URL)

		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		relay, err := newRelayInternal(cfg, relayInternalOptions{
			loggers:       mockLog.Loggers,
			clientFactory: testclient.CreateDummyClient,
		})
		require.NoError(t, err)
		defer relay.Close()

		// 3. Wait for the env to become available (confirms Relay processed the RAC put event).
		h := relayTestHelper{t: t, relay: relay}
		env := h.awaitEnvironment(harnessEnvID)

		// awaitEnvironment only waits until the env is discoverable by credential lookup. The SDK
		// client is created in a background goroutine (go c.startSDKClient(...)), so GetClient() can
		// still be nil at this point. Relay's stream middleware returns 503 (Service Unavailable)
		// while GetClient() == nil, which would cause the SSE request below to fail intermittently.
		// Wait for the client to be ready before connecting, mirroring the readiness poll in
		// internal/relayenv/env_context_impl_test.go (TestChangeSDKKey).
		require.Eventually(t, func() bool {
			return env.GetClient() != nil
		}, 5*time.Second, time.Millisecond*5, "timed out waiting for the SDK client to be ready")

		// 4. Connect to Relay's SSE stream and verify it serves a put event.
		req := sharedtest.BuildRequestWithAuth(http.MethodGet, "/all", harnessSDKKey, nil)
		sharedtest.WithStreamRequest(t, req, relay, func(eventCh <-chan eventsource.Event) {
			event := sharedtest.AwaitEventOfType(t, eventCh, "put", 5*time.Second)
			assert.NotNil(t, event, "expected Relay to serve a put event on the SSE stream")
		})
	})
}
