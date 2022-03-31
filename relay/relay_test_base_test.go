package relay

import (
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/require"
)

// Options for withStartedRelayCustom.
type relayTestBehavior struct {
	// All of the following are opt-in so the false behavior is the one we're most likely to use in tests.
	skipWaitForEnvironments bool // true = we're using auto-config or expect startup to fail; false = wait for all environments
	useRealSDKClient        bool // true = use real end-to-end HTTP; false = use a mock SDK client
	doNotEnableDebugLogging bool // true = leave the default log level in place; false = enable debug logging
}

// Components that are passed from withStartedRelay/withStartedRelayCustom to the test logic.
type relayTestParams struct {
	relay   *Relay
	mockLog *ldlogtest.MockLog
}

// withStartedRelay initializes a Relay instance, runs a block of test code against it, and then
// ensures that everything is cleaned up.
//
// Log output is redirected to a MockLog which can be read by tests.
//
// Normally, the Relay instance will use testclient.CreateDummyClient to substitute a test
// fixture for the SDK client. However, for tests that really want to do HTTP, if you set any
// of the BaseURI properties in the configuration, it will use a real SDK client.
func withStartedRelay(t *testing.T, config c.Config, action func(relayTestParams)) {
	withStartedRelayCustom(t, config, relayTestBehavior{}, action)
}

// withStartedRelayCustom is the same as withStartedRelay but allows more customization of the
// test setup.
func withStartedRelayCustom(t *testing.T, config c.Config, behavior relayTestBehavior, action func(relayTestParams)) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	if !config.Main.LogLevel.IsDefined() && !behavior.doNotEnableDebugLogging {
		config.Main.LogLevel = c.NewOptLogLevel(ldlog.Debug)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)
	}
	options := relayInternalOptions{loggers: mockLog.Loggers}
	if !behavior.useRealSDKClient {
		options.clientFactory = testclient.CreateDummyClient
	}
	relay, err := newRelayInternal(config, options)
	require.NoError(t, err)
	defer relay.Close()
	if !behavior.skipWaitForEnvironments {
		require.NoError(t, relay.core.WaitForAllClients(time.Second))
	}

	action(relayTestParams{
		relay:   relay,
		mockLog: mockLog,
	})
}
