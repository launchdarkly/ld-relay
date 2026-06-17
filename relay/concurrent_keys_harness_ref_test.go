package relay

// TestConcurrentKeysHarnessReference is the reference integration test for the Phase 1
// test harness (SDK-2548 / T5.a). It demonstrates all three harness components working
// together end-to-end:
//
//   - ArchiveFixtureBuilder (offline-mode archive with flag data)
//   - SDKSimulator (downstream SDK connecting to relay's SSE stream)
//   - RACMock (RAC SSE server delivering environment configuration)
//
// See internal/testharness/README.md for component documentation.

import (
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/testharness"

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

// TestConcurrentKeysHarnessReference exercises all three harness components.
func TestConcurrentKeysHarnessReference(t *testing.T) {
	t.Run("archive fixture + SDK simulator: flag data flows through relay's SSE stream", func(t *testing.T) {
		// 1. Build an offline-mode archive containing a single env with a simple boolean flag.
		archivePath := testharness.NewArchiveFixtureBuilder().
			AddEnv(testharness.ArchiveEnvSpec{
				Rep:    harnessEnvRep,
				DataID: "data-v1",
				Flags: map[string]any{
					harnessFlagKey: ldbuilders.NewFlagBuilder(harnessFlagKey).Version(1).On(true).Build(),
				},
			}).
			WriteTempFile(t)

		// 2. Start relay in offline mode with the real archive manager so the flag data
		//    actually flows through the data store and into the SSE stream.
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
		// Register relay cleanup first (runs last due to t.Cleanup LIFO order).
		t.Cleanup(func() { _ = relay.Close() })

		// Drain the client creation notification; in offline mode the archive manager
		// loads synchronously so the client should already be in the channel.
		_ = helpers.RequireValue(t, clientsCreatedCh, 3*time.Second, "timed out waiting for SDK client creation")

		// 3. Wrap relay in a test HTTP server.
		// Register server cleanup before the simulator's cleanup so that LIFO ordering
		// closes the simulator first, letting the server drain its connection cleanly.
		server := httptest.NewServer(relay)
		t.Cleanup(server.Close)

		// 4. Connect an SDK simulator to relay's server-side SSE stream.
		// NewSDKSimulator registers t.Cleanup(sim.Close) — registered after server.Close,
		// so it runs FIRST (LIFO), ensuring the open SSE connection is closed before the
		// server attempts to shut down.
		sim := testharness.NewSDKSimulator(t, server, harnessSDKKey)

		// 5. Verify the initial put event arrives and contains the flag.
		event := sim.AwaitEventOfType(t, "put", 5*time.Second)
		require.NotNil(t, event)
		assert.Contains(t, event.Data(), harnessFlagKey,
			"expected put event data to contain the flag key")
	})

	t.Run("RAC mock + SDK simulator: relay discovers env from RAC and SDK simulator connects", func(t *testing.T) {
		// 1. Create a RAC mock pre-loaded with a put event for the test environment.
		putEvent := testharness.MakePutEvent(harnessEnvRep)
		racMock := testharness.NewRACMock(t, &putEvent)

		// 2. Start relay configured to use the RAC mock as its config stream.
		// Use CreateDummyClient (rather than FakeLDClientFactory) so the data store is
		// initialized with sharedtest.AllData — this is required for relay to emit a put
		// event on the SSE stream when the simulator connects.
		cfg := c.Config{AutoConfig: c.AutoConfigConfig{Key: testAutoConfKey}}
		cfg.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(racMock.URL)

		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)

		relay, err := newRelayInternal(cfg, relayInternalOptions{
			loggers:       mockLog.Loggers,
			clientFactory: testclient.CreateDummyClient,
		})
		require.NoError(t, err)
		// Register relay cleanup first (runs last due to t.Cleanup LIFO order).
		t.Cleanup(func() { _ = relay.Close() })

		// 3. Wait for the env to become available (confirms relay processed the RAC put
		//    event and registered the environment).
		h := relayTestHelper{t: t, relay: relay}
		_ = h.awaitEnvironment(harnessEnvID)

		// 4. Wrap relay in a test HTTP server; same LIFO ordering as above.
		server := httptest.NewServer(relay)
		t.Cleanup(server.Close)

		sim := testharness.NewSDKSimulator(t, server, harnessSDKKey)

		// 5. Verify the simulator receives a put event (confirms the SSE stream is live).
		event := sim.AwaitEventOfType(t, "put", 5*time.Second)
		assert.NotNil(t, event, "expected SDK simulator to receive a put event from relay")
	})
}
