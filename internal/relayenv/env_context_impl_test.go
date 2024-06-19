package relayenv

import (
	"context"
	"errors"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
)

const envName = "envname"

func requireEnvReady(t *testing.T, readyCh <-chan EnvContext) EnvContext {
	return helpers.RequireValue(t, readyCh, time.Second, "timed out waiting for environment")
}

func requireClientReady(t *testing.T, clientCh chan *testclient.FakeLDClient) *testclient.FakeLDClient {
	return helpers.RequireValue(t, clientCh, time.Second, "timed out waiting for client")
}

func makeBasicEnv(t *testing.T, envConfig config.EnvConfig, clientFactory sdks.ClientFactoryFunc,
	loggers ldlog.Loggers, readyCh chan EnvContext) EnvContext {
	return makeBasicEnvWithMockTime(t, envConfig, clientFactory, loggers, readyCh, nil)
}

type mockConnectionMapper struct {
}

func (m mockConnectionMapper) AddConnectionMapping(scopedCredential sdkauth.ScopedCredential, envContext EnvContext) {

}
func (m mockConnectionMapper) RemoveConnectionMapping(scopedCredential sdkauth.ScopedCredential) {

}

func makeBasicEnvWithMockTime(t *testing.T, envConfig config.EnvConfig, clientFactory sdks.ClientFactoryFunc,
	loggers ldlog.Loggers, readyCh chan EnvContext, now func() time.Time) EnvContext {
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:        envConfig,
		ClientFactory:    clientFactory,
		Loggers:          loggers,
		TimeSource:       now,
		ConnectionMapper: mockConnectionMapper{},
	}, readyCh)
	require.NoError(t, err)
	return env
}

func TestConstructorBasicProperties(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, envName, env.GetIdentifiers().ConfiguredName)
	assert.Equal(t, time.Hour, env.GetTTL())
	assert.True(t, env.IsSecureMode())
	assert.Nil(t, env.GetEventDispatcher())                        // events were not enabled
	assert.Equal(t, context.Background(), env.GetMetricsContext()) // metrics aren't being used

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
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
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
		Loggers:         ldlog.NewDisabledLoggers(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	assert.Equal(t, jsClientContext, env.GetJSClientContext())
}

func TestLogPrefix(t *testing.T) {
	testPrefix := func(desc string, mode LogNameMode, sdkKey config.SDKKey, envID config.EnvironmentID, expected string) {
		t.Run(desc, func(t *testing.T) {
			envConfig := config.EnvConfig{SDKKey: sdkKey, EnvID: envID}
			mockLog := ldlogtest.NewMockLog()
			env, err := NewEnvContext(EnvContextImplParams{
				Identifiers:   EnvIdentifiers{ConfiguredName: "name"},
				EnvConfig:     envConfig,
				ClientFactory: testclient.FakeLDClientFactory(true),
				UserAgent:     "user-agent",
				LogNameMode:   mode,
				Loggers:       mockLog.Loggers,
			}, nil)
			require.NoError(t, err)
			defer env.Close()
			envImpl := env.(*envContextImpl)
			envImpl.loggers.Error("message")
			mockLog.AssertMessageMatch(t, true, ldlog.Error, "^"+regexp.QuoteMeta(expected)+" message")
		})
	}

	testPrefix("SDK key", LogNameIsSDKKey, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...7890]")
	testPrefix("env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...ghij]")
	testPrefix("env ID not set", LogNameIsEnvID, config.SDKKey("1234567890"), "", "[env: ...7890]")
	testPrefix("impossibly short SDK key", LogNameIsSDKKey, config.SDKKey("890"), config.EnvironmentID("abcdefghij"), "[env: 890]")
	testPrefix("impossibly short env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("hij"), "[env: hij]")
}

func TestAddRemoveCredential(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, nil)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	env.RotateMobileKey(st.EnvWithAllCredentials.Config.MobileKey)
	env.RotateEnvironmentID(st.EnvWithAllCredentials.Config.EnvID)

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.EnvID)

	env.RotateMobileKey("foo")

	creds = env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.NotContains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.EnvID)
}

func TestAddExistingCredentialDoesNothing(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, nil)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	env.RotateMobileKey(st.EnvWithAllCredentials.Config.MobileKey)

	creds := env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)

	env.RotateMobileKey(st.EnvWithAllCredentials.Config.MobileKey)

	creds = env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, st.EnvWithAllCredentials.Config.MobileKey)
}

func TestChangeSDKKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	key2 := config.SDKKey("key2")
	key3 := config.SDKKey("key3")

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	assert.Equal(t, env.GetClient(), client1)
	assert.Nil(t, env.GetInitError())

	env.RotateSDKKey(key2, credential.NewDeprecationNotice(envConfig.SDKKey, time.Now().Add(1*time.Hour)))

	assert.Equal(t, []credential.SDKCredential{key2}, env.GetCredentials())
	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetDeprecatedCredentials())

	client2 := requireClientReady(t, clientCh)
	assert.NotEqual(t, client1, client2)
	assert.Equal(t, env.GetClient(), client2)

	if !helpers.AssertChannelNotClosed(t, client1.CloseCh, 1*time.Second, "client for envConfig.SDKKey should not have been closed yet") {
		t.FailNow()
	}

	env.RotateSDKKey(key3, nil)
	assert.Equal(t, []credential.SDKCredential{key3}, env.GetCredentials())
	assert.ElementsMatch(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetDeprecatedCredentials())

	if !helpers.AssertChannelClosed(t, client2.CloseCh, 1*time.Second, "client for key2 should have been closed") {
		t.FailNow()
	}

}

func TestSDKClientCreationFails(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	fakeError := errors.New("sorry")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.ClientFactoryThatFails(fakeError), mockLog.Loggers, readyCh)
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
	// We already have tests for openCensusEventsExporter in the metrics package, but this test verifies that
	// exporting is configured automatically for every environment that we add (if not disabled).
	view.SetReportingPeriod(time.Millisecond * 10)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	fakeUserAgent := "fake-user-agent"

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		metricsManager, err := metrics.NewManager(config.MetricsConfig{}, time.Minute, mockLog.Loggers)
		require.NoError(t, err)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:    EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:      st.EnvMain.Config,
			AllConfig:      allConfig,
			ClientFactory:  testclient.FakeLDClientFactory(true),
			MetricsManager: metricsManager,
			UserAgent:      fakeUserAgent,
			Loggers:        mockLog.Loggers,
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)
		metrics.WithCount(env.GetMetricsContext(), fakeUserAgent, func() {
			require.Eventually(t, func() bool {
				flushMetricsEvents(envImpl)
				select {
				case req := <-requestsCh:
					mockLog.Loggers.Infof("received metrics events: %s", req.Body)
					data := ldvalue.Parse(req.Body)
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
	view.SetReportingPeriod(time.Millisecond * 10)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	fakeUserAgent := "fake-user-agent"

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		metricsManager, err := metrics.NewManager(config.MetricsConfig{}, time.Minute, mockLog.Loggers)
		require.NoError(t, err)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:    EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:      st.EnvMain.Config,
			AllConfig:      allConfig,
			ClientFactory:  testclient.FakeLDClientFactory(true),
			MetricsManager: metricsManager,
			Loggers:        mockLog.Loggers,
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)
		metrics.WithCount(env.GetMetricsContext(), fakeUserAgent, func() {
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
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

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
			Loggers:       mockLog.Loggers,
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
		require.Equal(t, string(body), string(eventPost.Body))
	})
}

func TestEventDispatcherIsNotCreatedIfSendEventsIsTrueAndNotInOfflineMode(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

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
			Loggers:       mockLog.Loggers,
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

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

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
		Loggers: mockLog.Loggers,
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

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

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
		Loggers: mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)
	assert.False(t, synchronizer.isStarted())

	// Simulate receiving some data
	updates := env.(*envContextImpl).storeAdapter.GetUpdates()

	s1 := ldbuilders.NewSegmentBuilder("s1").Build()
	dataWithNoBigSegment := []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: "s1", Item: st.SegmentDesc(s1)},
			},
		},
	}
	updates.SendAllDataUpdate(dataWithNoBigSegment)

	assert.False(t, synchronizer.isStarted())

	s2 := ldbuilders.NewSegmentBuilder("s2").Unbounded(true).Generation(1).Build()
	dataWithBigSegment := []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: "s1", Item: st.SegmentDesc(s1)},
				{Key: "s2", Item: st.SegmentDesc(s2)},
			},
		},
	}
	updates.SendAllDataUpdate(dataWithBigSegment)

	assert.True(t, synchronizer.isStarted())

	// Now we should expose the big segment store so that Relay can include big segment status information
	// in its status resource.
	assert.NotNil(t, env.GetBigSegmentStore())
}

func TestBigSegmentsSynchronizerIsStartedBySingleItemUpdateWithBigSegment(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

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
		Loggers: mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)
	assert.False(t, synchronizer.isStarted())

	// Simulate receiving some data
	updates := env.(*envContextImpl).storeAdapter.GetUpdates()

	f1 := ldbuilders.NewFlagBuilder("f1").Build()
	updates.SendSingleItemUpdate(ldstoreimpl.Features(), f1.Key, st.FlagDesc(f1))

	assert.False(t, synchronizer.isStarted())

	s1 := ldbuilders.NewSegmentBuilder("s1").Build()
	updates.SendSingleItemUpdate(ldstoreimpl.Segments(), s1.Key, st.SegmentDesc(s1))

	assert.False(t, synchronizer.isStarted())

	s2 := ldbuilders.NewSegmentBuilder("s2").Unbounded(true).Generation(1).Build()
	updates.SendSingleItemUpdate(ldstoreimpl.Segments(), s2.Key, st.SegmentDesc(s2))

	assert.True(t, synchronizer.isStarted())
}

func TestReceivingBigSegmentsUpdateCausesClientSideInvalidationEvent(t *testing.T) {
	envConfig := st.EnvClientSide.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	jsClientStreams := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour)
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
		Loggers:         mockLog.Loggers,
	}, sdkStartedCh)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)

	streamHandler := env.GetStreamHandler(jsClientStreams, envConfig.EnvID)

	// Make sure the data store is initialized, otherwise the client-side endpoint won't broadcast a ping
	<-sdkStartedCh
	_ = env.GetStore().Init(nil)

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
	loggers ldlog.Loggers,
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
