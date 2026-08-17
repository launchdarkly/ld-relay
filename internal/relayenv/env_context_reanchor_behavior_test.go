package relayenv

// Permanent behavioral regression tests for re-anchoring. These pin two properties of the
// upstream-client swap:
//
//   - an open downstream (client-side) connection survives a re-anchor and keeps receiving events,
//     with the re-wired big-segment synchronizer driving its invalidations;
//   - httpconfig carries no baked-in SDK key other than the Authorization header the SDK sets per client,
//     so it needs no re-wiring on a re-anchor.

import (
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReanchorDownstreamConnectionSurvives verifies that an open downstream client-side connection
// survives a re-anchor and keeps receiving events. The connection is keyed on the environment ID (a
// scoped credential) and is independent of the upstream SDK anchor key, so swapping the anchor must not
// disturb it. After the re-anchor the big-segment synchronizer has been rebuilt on the new anchor, so a
// big-segment update delivered on the current (rebuilt) synchronizer still pings the connected client.
func TestReanchorDownstreamConnectionSurvives(t *testing.T) {
	envConfig := st.EnvClientSide.Config

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	jsClientStreams := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)
	clientCh := make(chan *testclient.FakeLDClient, 10)
	sdkStartedCh := make(chan EnvContext, 10)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvClientSide.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     config.Config{},
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactoryWithChannel(true, clientCh),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		StreamProviders:  []streams.StreamProvider{jsClientStreams},
		ConnectionMapper: mockConnectionMapper{},
		Loggers:          mockLog.Loggers,
	}, sdkStartedCh)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)

	// Wait for the original anchor client and initialize the store so the client-side stream is ready.
	<-sdkStartedCh
	require.NoError(t, env.GetStore().Init(nil))

	streamHandler := env.GetStreamHandler(jsClientStreams, envConfig.EnvID)
	req, _ := http.NewRequest("GET", "", nil)
	st.WithStreamRequest(t, req, streamHandler, func(eventCh <-chan eventsource.Event) {
		initEvent := helpers.RequireValue(t, eventCh, time.Minute)
		assert.Equal(t, "ping", initEvent.Event())
		if !helpers.AssertNoMoreValues(t, eventCh, 100*time.Millisecond) {
			t.FailNow()
		}

		// Re-anchor while the downstream connection is open. The re-anchor runs synchronously, so the
		// new anchor's client is built and committed and the big-segment synchronizer is rebuilt by the
		// time this returns.
		now := time.Unix(1000, 0)
		reanchorViaReconcile(t, env, reanchorSyncTestKey2, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)

		// The synchronizer was rebuilt on the new anchor, so a post-re-anchor update arrives on the
		// current (rebuilt) synchronizer, not the retired one (whose channel is now closed).
		current := fakeSynchronizerFactory.synchronizer
		require.NotSame(t, synchronizer, current, "the synchronizer was rebuilt on re-anchor")
		current.updateCh <- bigsegments.UpdatesSummary{SegmentKeysUpdated: []string{"fake-segment-key"}}
		pingEvent := helpers.RequireValue(t, eventCh, time.Second)
		assert.Equal(t, "ping", pingEvent.Event(), "downstream connection should survive the re-anchor")
	})
}

// TestReanchorHTTPConfigIsKeyIndependent verifies that httpconfig carries no baked-in SDK key other than
// the Authorization default header, so a re-anchor needs no httpconfig re-wire. Relay injects the SDK
// HTTP config *builder* into the SDK config, and the SDK rebuilds the HTTP config with the new anchor key
// when it constructs the new client, so the Authorization header is set correctly for the new anchor
// automatically. The pre-built SDK HTTP config used for event and big-segment transport is
// key-independent except for that Authorization header, which those components set per request from
// their own credential rather than reading it from httpconfig.
func TestReanchorHTTPConfigIsKeyIndependent(t *testing.T) {
	loggers := ldlog.NewDisabledLoggers()
	key1 := config.SDKKey("sdk-key-one")
	key2 := config.SDKKey("sdk-key-two")

	var proxy config.ProxyConfig
	var httpC config.HTTPConfig

	c1, err := httpconfig.NewHTTPConfig(proxy, httpC, key1, "user-agent", loggers)
	require.NoError(t, err)
	c2, err := httpconfig.NewHTTPConfig(proxy, httpC, key2, "user-agent", loggers)
	require.NoError(t, err)

	// The only key-dependent artifact is the Authorization default header on the pre-built SDK HTTP config.
	assert.Equal(t, string(key1), c1.SDKHTTPConfig.DefaultHeaders.Get("Authorization"))
	assert.Equal(t, string(key2), c2.SDKHTTPConfig.DefaultHeaders.Get("Authorization"))

	// Everything else (proxy settings, user agent, and the rest of the default headers) is identical and
	// key-independent.
	h1 := c1.SDKHTTPConfig.DefaultHeaders.Clone()
	h2 := c2.SDKHTTPConfig.DefaultHeaders.Clone()
	h1.Del("Authorization")
	h2.Del("Authorization")
	assert.Equal(t, h1, h2, "non-auth default headers are key-independent")
	assert.Equal(t, c1.ProxyConfig, c2.ProxyConfig, "proxy config is key-independent")
}
