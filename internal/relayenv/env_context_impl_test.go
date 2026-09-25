package relayenv

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"

	"github.com/launchdarkly/ld-relay/v9/internal/streams"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/ld-relay/v9/internal/credential"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v9/internal/events"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envName = "envname"

func requireEnvReady(t *testing.T, readyCh <-chan EnvContext) EnvContext {
	return helpers.RequireValue(t, readyCh, time.Second, "timed out waiting for environment")
}

func requireClientReady(t *testing.T, clientCh chan *testclient.FakeLDClient) *testclient.FakeLDClient {
	return helpers.RequireValue(t, clientCh, time.Second, "timed out waiting for client")
}

func makeBasicEnv(t *testing.T, envConfig config.EnvConfig, clientFactory sdks.ClientFactoryFunc,
	logger *slog.Logger, readyCh chan EnvContext,
) EnvContext {
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:        envConfig,
		ClientFactory:    clientFactory,
		Logger:           logger,
		ConnectionMapper: mockConnectionMapper{},
	}, readyCh)
	require.NoError(t, err)
	return env
}

type mockConnectionMapper struct{}

func (m mockConnectionMapper) AddConnectionMapping(scopedCredential credential.SDKCredential, envContext EnvContext) {
}

func (m mockConnectionMapper) RemoveConnectionMapping(scopedCredential credential.SDKCredential) {
}

func TestConstructorBasicProperties(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh, nil)

	env := makeBasicEnv(t, envConfig, clientFactory, slog.Default(), readyCh)
	defer env.Close()

	assert.Equal(t, envName, env.GetIdentifiers().ConfiguredName)
	assert.Equal(t, time.Hour, env.GetTTL())
	assert.True(t, env.IsSecureMode())
	assert.Nil(t, env.GetEventDispatcher()) // events were not enabled
	assert.Nil(t, env.GetMetricsEnv())      // metrics aren't being used

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, envConfig.MobileKey)
	assert.Contains(t, creds, envConfig.EnvID)

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, env.GetClient(), requireClientReady(t, clientCh))
	assert.Nil(t, env.GetInitError())

	assert.NotNil(t, env.GetStore())
}

func TestConstructorWithOnlySDKKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh, nil)

	env := makeBasicEnv(t, envConfig, clientFactory, slog.Default(), readyCh)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, env.GetClient(), requireClientReady(t, clientCh))
	assert.Nil(t, env.GetInitError())
}

func TestConstructorWithJSClientContext(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	jsClientContext := JSClientContext{Origins: []string{"origin"}}
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:     EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:       envConfig,
		ClientFactory:   testclient.FakeLDClientFactory(true),
		JSClientContext: jsClientContext,
		Logger:          slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	assert.Equal(t, jsClientContext, env.GetJSClientContext())
}

func TestLogPrefix(t *testing.T) {
	testPrefix := func(desc string, mode LogNameMode, sdkKey config.SDKKey, envID config.EnvironmentID, expected string) {
		t.Run(desc, func(t *testing.T) {
			prefix := makeLogPrefix(mode, sdkKey, envID)
			assert.Equal(t, expected, prefix)
		})
	}

	testPrefix("SDK key", LogNameIsSDKKey, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...7890]")
	testPrefix("env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...ghij]")
	testPrefix("env ID not set", LogNameIsEnvID, config.SDKKey("1234567890"), "", "[env: ...7890]")
	testPrefix("impossibly short SDK key", LogNameIsSDKKey, config.SDKKey("890"), config.EnvironmentID("abcdefghij"), "[env: 890]")
	testPrefix("impossibly short env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("hij"), "[env: hij]")
}

func TestReconcileAddsAndRemovesCredentials(t *testing.T) {
	envConfig := st.EnvMain.Config
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), slog.Default(), nil)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	mobile := st.EnvWithAllCredentials.Config.MobileKey
	envID := st.EnvWithAllCredentials.Config.EnvID
	env.ReconcileCredentials(mustAcceptedSet(t, envConfig.SDKKey, mobile, envID))

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, mobile)
	assert.Contains(t, creds, envID)
	assert.Equal(t, mobile, env.GetMobileKey())

	// A set that names a different mobile key revokes the previous one.
	replacement := config.MobileKey("evict-the-previous-key")
	env.ReconcileCredentials(mustAcceptedSet(t, envConfig.SDKKey, replacement, envID))

	creds = env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.NotContains(t, creds, mobile)
	assert.Contains(t, creds, replacement)
	assert.Contains(t, creds, envID)
	assert.Equal(t, replacement, env.GetMobileKey())
}

func TestReconcileWithAnUnchangedSetChangesNothing(t *testing.T) {
	envConfig := st.EnvMain.Config
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), slog.Default(), nil)
	defer env.Close()

	mobile := st.EnvWithAllCredentials.Config.MobileKey
	set := mustAcceptedSet(t, envConfig.SDKKey, mobile, "")

	env.ReconcileCredentials(set)
	first := env.GetCredentials()
	assert.Len(t, first, 2)

	env.ReconcileCredentials(set)
	assert.ElementsMatch(t, first, env.GetCredentials())
	assert.Equal(t, envConfig.SDKKey, env.GetAnchorKey())
}

// TestRotatingTheSDKKeyRekeysTheSameClient is the central behavioral test for the re-anchor.
//
// The environment holds exactly one SDK client for its lifetime. A key rotation re-keys that client
// rather than building a replacement, which is what keeps relay from holding two data systems and two
// copies of the environment's data. The outgoing key keeps authenticating downstream traffic for its
// grace period, and its expiry must not take the client down with it.
func TestRotatingTheSDKKeyRekeysTheSameClient(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	newKey := config.SDKKey("key2")

	clientCh := make(chan *testclient.FakeLDClient, 1)
	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh, nil),
		slog.Default(), readyCh)
	defer env.Close()

	require.Equal(t, env, requireEnvReady(t, readyCh))
	client := requireClientReady(t, clientCh)
	require.Equal(t, env.GetClient(), client)
	require.Equal(t, envConfig.SDKKey, client.CurrentSDKKey())
	require.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())
	require.Empty(t, env.GetDeprecatedCredentials())

	start := time.Unix(1000, 0)
	expiry := start.Add(1 * time.Hour)
	impl := env.(*envContextImpl)

	// Rotate to newKey, with the outgoing key accepted for another hour.
	impl.reconcileCredentials(mustAcceptedSetWithExpiring(t, newKey, envConfig.SDKKey, expiry), start)

	// The anchor moved and the same client was re-keyed. No second client was built.
	assert.Equal(t, newKey, env.GetAnchorKey())
	assert.Same(t, client, env.GetClient(), "a rotation must not replace the environment's client")
	assert.Equal(t, newKey, client.CurrentSDKKey())
	assert.Equal(t, []config.SDKKey{newKey}, client.SDKKeys())
	helpers.AssertNoMoreValues(t, clientCh, time.Millisecond*100, "no second client should be built")

	// Both keys authenticate during the grace period.
	creds := env.GetCredentials()
	assert.Contains(t, creds, newKey)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetDeprecatedCredentials())

	// Part-way through the grace period nothing changes.
	impl.triggerCredentialChanges(start.Add(45 * time.Minute))
	assert.Contains(t, env.GetCredentials(), envConfig.SDKKey)

	// An instant past the expiry the outgoing key stops authenticating, and the client survives.
	impl.triggerCredentialChanges(expiry.Add(time.Millisecond))
	assert.Equal(t, []credential.SDKCredential{newKey}, env.GetCredentials())
	assert.Empty(t, env.GetDeprecatedCredentials())
	assert.Same(t, client, env.GetClient(),
		"expiring the key the client was built with must not close the client")
	if !helpers.AssertChannelNotClosed(t, client.CloseCh, 100*time.Millisecond,
		"the environment's only client must stay open across a rotation") {
		t.FailNow()
	}
}

// TestReanchorKeepsThePreviousKeyWhenTheSDKRejectsTheNewOne covers the one way a re-anchor can fail.
// SetSDKKey rejects a key that is not valid in an HTTP header, which no retry would fix, so the
// environment parks on the key it has rather than losing its upstream connection.
func TestReanchorKeepsThePreviousKeyWhenTheSDKRejectsTheNewOne(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	clientCh := make(chan *testclient.FakeLDClient, 1)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactoryWithChannel(true, clientCh, nil),
		slog.Default(), readyCh)
	defer env.Close()
	requireEnvReady(t, readyCh)
	client := requireClientReady(t, clientCh)

	client.SetSDKKeyErr = errors.New("SDK key contains invalid characters")

	rejected := config.SDKKey("bad key")
	impl := env.(*envContextImpl)
	impl.reconcileCredentials(mustAcceptedSet(t, rejected, "", ""), time.Unix(1000, 0))

	assert.Equal(t, envConfig.SDKKey, env.GetAnchorKey(), "the anchor must not move")
	assert.Same(t, client, env.GetClient())
	assert.Contains(t, env.GetCredentials(), envConfig.SDKKey,
		"the previous key must keep authenticating, since it is what the connection still uses")
	assert.NotContains(t, env.GetCredentials(), rejected,
		"a key the SDK refused must not be left accepted")
}

// mustAcceptedSet builds an accepted set with anchor as the designated SDK key. An undefined mobile
// key or environment ID is omitted.
func mustAcceptedSet(t *testing.T, anchor config.SDKKey, mobile config.MobileKey, envID config.EnvironmentID) credential.AcceptedSet {
	t.Helper()
	b := credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: anchor})
	if mobile.Defined() {
		b.WithPrimaryMobileKey(credential.MobileKeyParams{Value: mobile})
	}
	if envID.Defined() {
		b.WithEnvironmentID(envID)
	}
	set, err := b.Build()
	require.NoError(t, err)
	return set
}

// mustAcceptedSetWithExpiring builds an accepted set with anchor designated and expiring accepted
// until the given instant, which is the shape of a key rotation with a grace period.
func mustAcceptedSetWithExpiring(t *testing.T, anchor config.SDKKey, expiring config.SDKKey, expiry time.Time) credential.AcceptedSet {
	t.Helper()
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: anchor}).
		WithSDKKey(credential.SDKKeyParams{Value: expiring, Expiry: &expiry}).
		Build()
	require.NoError(t, err)
	return set
}
func TestSDKClientCreationFails(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	fakeError := errors.New("sorry")

	env := makeBasicEnv(t, envConfig, testclient.ClientFactoryThatFails(fakeError), slog.Default(), readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, fakeError, env.GetInitError())
	assert.Nil(t, env.GetStore())
}

func TestDisplayName(t *testing.T) {
	ei1 := EnvIdentifiers{ProjName: "a", EnvName: "b", ConfiguredName: "thing"}
	assert.Equal(t, "thing", ei1.GetDisplayName())

	ei2 := EnvIdentifiers{ProjName: "a", EnvName: "b"}
	assert.Equal(t, "a b", ei2.GetDisplayName())
}

func TestMetricsAreExportedForEnvironment(t *testing.T) {
	// We already have tests for the relay metrics collector in the metrics package, but this test verifies that
	// exporting is configured automatically for every environment that we add (if not disabled).

	fakeUserAgent := "fake-user-agent"

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		metricsManager, err := metrics.NewManager(config.OpenTelemetryConfig{}, time.Minute, slog.Default())
		require.NoError(t, err)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:    EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:      st.EnvMain.Config,
			AllConfig:      allConfig,
			ClientFactory:  testclient.FakeLDClientFactory(true),
			MetricsManager: metricsManager,
			UserAgent:      fakeUserAgent,
			Logger:         slog.Default(),
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)
		metrics.WithStreamConnection(env.GetMetricsEnv(), metrics.RequestInfo{UserAgent: fakeUserAgent, Route: "/test", Method: "GET"}, func() {
			require.Eventually(t, func() bool {
				flushMetricsEvents(envImpl)
				select {
				case req := <-requestsCh:
					slog.Default().Info("received metrics events", "body", string(req.Body))
					uncompressed, err := util.DecompressGzipData(req.Body)
					require.NoError(t, err)

					data := ldvalue.Parse(uncompressed)
					event := data.GetByIndex(0)
					if !event.IsNull() {
						conns := event.GetByKey("connections")
						return event.GetByKey("kind").StringValue() == "relayMetrics" &&
							conns.Count() == 1 &&
							conns.GetByIndex(0).GetByKey("userAgent").StringValue() == fakeUserAgent &&
							conns.GetByIndex(0).GetByKey("current").IntValue() == 1
					}
				default:
					break
				}
				return false
			}, time.Second, time.Millisecond*10, "timed out waiting for metrics event with counter")
		}, metrics.BrowserConns)
	})
}

func TestMetricsAreNotExportedForEnvironmentInOfflineMode(t *testing.T) {
	var allConfig config.Config
	allConfig.OfflineMode.FileDataSource = "fake-file-path"
	testMetricsDisabled(t, allConfig)
}

func testMetricsDisabled(t *testing.T, allConfig config.Config) {

	fakeUserAgent := "fake-user-agent"

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		metricsManager, err := metrics.NewManager(config.OpenTelemetryConfig{}, time.Minute, slog.Default())
		require.NoError(t, err)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:    EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:      st.EnvMain.Config,
			AllConfig:      allConfig,
			ClientFactory:  testclient.FakeLDClientFactory(true),
			MetricsManager: metricsManager,
			Logger:         slog.Default(),
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)
		metrics.WithStreamConnection(env.GetMetricsEnv(), metrics.RequestInfo{UserAgent: fakeUserAgent, Route: "/test", Method: "GET"}, func() {
			require.Never(t, func() bool {
				flushMetricsEvents(envImpl)
				select {
				case <-requestsCh:
					return true
				default:
					break
				}
				return false
			}, time.Millisecond*100, time.Millisecond*10, "received unexpected metrics event")
		}, metrics.BrowserConns)
	})
}

func TestEventDispatcherIsCreatedIfSendEventsIsTrueAndNotInOfflineMode(t *testing.T) {

	eventRecorderHandler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(eventRecorderHandler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.SendEvents = true
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		allConfig.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:   EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:     st.EnvMain.Config,
			AllConfig:     allConfig,
			ClientFactory: testclient.FakeLDClientFactory(true),
			Logger:        slog.Default(),
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)

		ed := envImpl.GetEventDispatcher()
		require.NotNil(t, ed)
		eventDispatchHandler := ed.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, eventDispatchHandler)

		rr := httptest.NewRecorder()
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Authorization", string(st.EnvMain.Config.SDKKey))
		headers.Set("X-LaunchDarkly-Event-Schema", strconv.Itoa(events.SummaryEventsSchemaVersion))
		body := `[{"kind":"identify","creationDate":1000,"key":"userkey","user":{"key":"userkey"}}]`
		req := st.BuildRequest("POST", server.URL+"/bulk", []byte(body), headers)
		eventDispatchHandler(rr, req)
		require.Equal(t, 202, rr.Result().StatusCode)

		// Because the event schema version is >= 3, the event data should be forwarded verbatim with no processing.
		eventPost := helpers.RequireValue(t, requestsCh, time.Second)
		require.Equal(t, string(st.EnvMain.Config.SDKKey), eventPost.Request.Header.Get("Authorization"))

		decodedBody, err := util.DecompressGzipData(eventPost.Body)
		require.NoError(t, err)
		require.Equal(t, string(body), string(decodedBody))
	})
}

func TestEventDispatcherIsNotCreatedIfSendEventsIsTrueAndNotInOfflineMode(t *testing.T) {

	eventRecorderHandler, _ := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(eventRecorderHandler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.OfflineMode.FileDataSource = "fake-file-path"
		allConfig.Events.SendEvents = true
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		allConfig.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:   EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:     st.EnvMain.Config,
			AllConfig:     allConfig,
			ClientFactory: testclient.FakeLDClientFactory(true),
			Logger:        slog.Default(),
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)

		ed := envImpl.GetEventDispatcher()
		require.Nil(t, ed)
	})
}

func TestBigSegmentsSynchronizerIsCreatedIfBigSegmentStoreExists(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, *slog.Logger) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		Logger: slog.Default(),
	}, nil)
	require.NoError(t, err)

	if assert.NotNil(t, fakeSynchronizerFactory.synchronizer) {
		assert.False(t, fakeSynchronizerFactory.synchronizer.isStarted())
		assert.False(t, fakeSynchronizerFactory.synchronizer.isClosed())

		// We shouldn't expose the store until some big segments exist, so that Relay doesn't report
		// misleading big segments status info in its status resource.
		assert.Nil(t, env.GetBigSegmentStore())
	}

	env.Close()

	assert.True(t, fakeSynchronizerFactory.synchronizer.isClosed())
}

func TestBigSegmentsSynchronizerIsStartedByFullDataUpdateWithBigSegment(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, *slog.Logger) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	changeSetCh := make(chan subsystems.ChangeSet, 1)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactoryWithChannel(true, nil, changeSetCh),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		Logger: slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)
	assert.False(t, synchronizer.isStarted())

	s1 := ldbuilders.NewSegmentBuilder("s1").Build()
	s1JSON, _ := json.Marshal(s1)

	changeSetBuilder := subsystems.NewChangeSetBuilder()
	changeSetBuilder.Start(subsystems.ServerIntent{
		Payload: subsystems.Payload{
			ID:     "new-state",
			Target: 1,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		},
	})
	changeSetBuilder.AddPut(subsystems.SegmentKind, s1.Key, s1.Version, s1JSON)
	changeSet, err := changeSetBuilder.Finish(subsystems.NewSelector("new-state", 1))
	require.NoError(t, err)

	changeSetCh <- *changeSet
	ensureSynchronizerState(t, synchronizer, false)

	s2 := ldbuilders.NewSegmentBuilder("s2").Unbounded(true).Generation(1).Build()
	s2JSON, _ := json.Marshal(s2)

	changeSetBuilder = subsystems.NewChangeSetBuilder()
	changeSetBuilder.Start(subsystems.ServerIntent{
		Payload: subsystems.Payload{
			ID:     "new-state",
			Target: 2,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		},
	})
	changeSetBuilder.AddPut(subsystems.SegmentKind, s1.Key, s1.Version, s1JSON)
	changeSetBuilder.AddPut(subsystems.SegmentKind, s2.Key, s2.Version, s2JSON)
	changeSet, err = changeSetBuilder.Finish(subsystems.NewSelector("new-state", 2))
	require.NoError(t, err)

	changeSetCh <- *changeSet
	ensureSynchronizerState(t, synchronizer, true)

	// Now we should expose the big segment store so that Relay can include big segment status information
	// in its status resource.
	require.Eventually(t, func() bool {
		return env.GetBigSegmentStore() != nil
	}, time.Second, 10*time.Millisecond, "timed out waiting for big segment store to be available")
}

func TestBigSegmentsSynchronizerIsStartedBySingleItemUpdateWithBigSegment(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, *slog.Logger) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	changeSetCh := make(chan subsystems.ChangeSet, 1)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactoryWithChannel(true, nil, changeSetCh),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		Logger: slog.Default(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)
	assert.False(t, synchronizer.isStarted())

	f1 := ldbuilders.NewFlagBuilder("f1").Build()
	testFlag1JSON, _ := json.Marshal(f1)

	changeSetBuilder := subsystems.NewChangeSetBuilder()
	changeSetBuilder.Start(subsystems.ServerIntent{
		Payload: subsystems.Payload{
			ID:     "new-state",
			Target: 1,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		},
	})
	changeSetBuilder.AddPut(subsystems.FlagKind, f1.Key, f1.Version, testFlag1JSON)
	changeSet, err := changeSetBuilder.Finish(subsystems.NewSelector("new-state", 1))
	require.NoError(t, err)

	changeSetCh <- *changeSet
	ensureSynchronizerState(t, synchronizer, false)

	s1 := ldbuilders.NewSegmentBuilder("s1").Build()
	testSegment1JSON, _ := json.Marshal(s1)

	changeSetBuilder.AddPut(subsystems.SegmentKind, s1.Key, s1.Version, testSegment1JSON)
	changeSet, err = changeSetBuilder.Finish(subsystems.NewSelector("new-state", 2))
	require.NoError(t, err)

	changeSetCh <- *changeSet
	ensureSynchronizerState(t, synchronizer, false)

	s2 := ldbuilders.NewSegmentBuilder("s2").Unbounded(true).Generation(1).Build()
	testSegment2JSON, _ := json.Marshal(s2)

	changeSetBuilder.AddPut(subsystems.SegmentKind, s2.Key, s2.Version, testSegment2JSON)
	changeSet, err = changeSetBuilder.Finish(subsystems.NewSelector("new-state", 3))
	require.NoError(t, err)

	changeSetCh <- *changeSet
	ensureSynchronizerState(t, synchronizer, true)
}

func TestReceivingBigSegmentsUpdateCausesClientSideInvalidationEvent(t *testing.T) {
	envConfig := st.EnvClientSide.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, *slog.Logger) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	jsClientStreams := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)
	sdkStartedCh := make(chan EnvContext)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		StreamProviders: []streams.StreamProvider{jsClientStreams},
		Logger:          slog.Default(),
	}, sdkStartedCh)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)

	streamHandler := env.GetStreamHandlerV1(jsClientStreams, envConfig.EnvID)

	// Make sure the data store is initialized, otherwise the client-side endpoint won't broadcast a ping
	<-sdkStartedCh

	req, _ := http.NewRequest("GET", "", nil)
	st.WithStreamRequest(t, req, streamHandler, func(eventCh <-chan eventsource.Event) {
		initEvent := helpers.RequireValue(t, eventCh, time.Minute)
		assert.Equal(t, "ping", initEvent.Event())

		if !helpers.AssertNoMoreValues(t, eventCh, time.Millisecond*100) {
			t.FailNow()
		}

		synchronizer.updateCh <- bigsegments.UpdatesSummary{SegmentKeysUpdated: []string{"fake-segment-key"}}

		pingEvent := helpers.RequireValue(t, eventCh, time.Second)
		assert.Equal(t, "ping", pingEvent.Event())
	})
}

// This method forces the metrics events exporter to post an event to the event publisher, and then triggers a
// flush of the event publisher. Because both of those actions are asynchronous, it may be necessary to call it
// more than once to ensure that the newly posted event is included in the flush.
func flushMetricsEvents(c *envContextImpl) {
	if c.metricsEventPub != nil {
		c.metricsEnv.FlushEventsExporter()
		c.metricsEventPub.Flush()
	}
}

type mockBigSegmentSynchronizerFactory struct {
	synchronizer *mockBigSegmentSynchronizer
}

func (f *mockBigSegmentSynchronizerFactory) create(
	httpConfig httpconfig.HTTPConfig,
	store bigsegments.BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	logger *slog.Logger,
	logPrefix string,
) bigsegments.BigSegmentSynchronizer {
	f.synchronizer = &mockBigSegmentSynchronizer{updateCh: make(chan bigsegments.UpdatesSummary)}
	return f.synchronizer
}

type mockBigSegmentSynchronizer struct {
	started  bool
	closed   bool
	updateCh chan bigsegments.UpdatesSummary
	lock     sync.Mutex
}

func (s *mockBigSegmentSynchronizer) Start() {
	s.lock.Lock()
	s.started = true
	s.lock.Unlock()
}

func (s *mockBigSegmentSynchronizer) HasSynced() bool {
	return true
}

func (s *mockBigSegmentSynchronizer) SegmentUpdatesCh() <-chan bigsegments.UpdatesSummary {
	return s.updateCh
}

func (s *mockBigSegmentSynchronizer) Close() {
	s.lock.Lock()
	s.closed = true
	s.lock.Unlock()
}

func (s *mockBigSegmentSynchronizer) isStarted() bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.started
}

func (s *mockBigSegmentSynchronizer) isClosed() bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.closed
}

func ensureSynchronizerState(t *testing.T, synchronizer *mockBigSegmentSynchronizer, expectedState bool) {
	require.Eventually(t, func() bool {
		return synchronizer.isStarted() == expectedState
	}, time.Second, 10*time.Millisecond, "timed out waiting for big segments synchronizer to start")
}
