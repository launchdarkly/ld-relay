package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	c "github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/basictypes"
	st "github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests validate the processing of requests through a full HTTP request-response cycle, using a
// real embedded HTTP server to host the Relay instance, but without actually connecting to LaunchDarkly.

var testFlag = ldbuilders.NewFlagBuilder("test-flag").Version(1).
	ClientSideUsingMobileKey(true).ClientSideUsingEnvironmentID(true). // ensures that it'll show up in all endpoints
	Build()

type relayEndToEndTestParams struct {
	relayTestParams
	t            *testing.T
	requestsCh   <-chan httphelpers.HTTPRequestInfo
	relayURL     string
	loggers      ldlog.Loggers
	singleLogger ldlog.BaseLogger
}

func relayEndToEndTest(
	t *testing.T,
	config c.Config,
	behavior relayTestBehavior,
	ldStreamHandler http.Handler,
	action func(p relayEndToEndTestParams),
) {
	relayMockLog := ldlogtest.NewMockLog()
	defer relayMockLog.DumpIfTestFailed(t)

	pollHandler := httphelpers.HandlerWithStatus(404)
	streamHandler, requestsCh := httphelpers.RecordingHandler(ldStreamHandler)
	eventsHandler := httphelpers.HandlerWithStatus(202)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			httphelpers.WithServer(eventsHandler, func(eventsServer *httptest.Server) {
				config.Main.BaseURI, _ = configtypes.NewOptURLAbsoluteFromString(pollServer.URL)
				config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
				config.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(eventsServer.URL)

				behavior.useRealSDKClient = true
				withStartedRelayCustom(t, config, behavior, func(p relayTestParams) {
					for _, env := range config.Environment {
						streamReq := helpers.RequireValue(t, requestsCh, time.Second*5)
						assert.Equal(t, string(env.SDKKey), streamReq.Request.Header.Get("Authorization"))
					}

					httphelpers.WithServer(p.relay, func(relayServer *httptest.Server) {
						mockLog := ldlogtest.NewMockLog()
						mockLog.Loggers.SetPrefix("TestClient:")
						defer mockLog.DumpIfTestFailed(t)
						p1 := relayEndToEndTestParams{
							relayTestParams: p,
							t:               t,
							requestsCh:      requestsCh,
							relayURL:        relayServer.URL,
							loggers:         mockLog.Loggers,
							singleLogger:    mockLog.Loggers.ForLevel(ldlog.Info),
						}
						action(p1)
					})
				})
			})
		})
	})
}

func (p relayEndToEndTestParams) waitForSuccessfulInit() {
	p.waitForLogMessage(ldlog.Info, "Initialized LaunchDarkly client for", "Relay initialization")
}

func (p relayEndToEndTestParams) waitForLogMessage(level ldlog.LogLevel, pattern, conditionDesc string) {
	require.Eventually(p.t, func() bool {
		return p.mockLog.HasMessageMatch(level, pattern)
	}, time.Second*2, time.Millisecond*50, "timed out waiting for %s", conditionDesc)
}

func (p relayEndToEndTestParams) subscribeStream(testEnv st.TestEnv, kind basictypes.StreamKind) (
	*eventsource.Stream, *eventsource.SubscriptionError) {
	req := st.MakeSDKStreamEndpointRequest(p.relayURL, kind, testEnv, st.SimpleUserJSON, 0)
	stream, err := eventsource.SubscribeWithRequestAndOptions(req, eventsource.StreamOptionLogger(p.singleLogger))
	if err != nil {
		require.IsType(p.t, eventsource.SubscriptionError{}, err)
		se := err.(eventsource.SubscriptionError)
		return nil, &se
	}
	return stream, nil
}

func (p relayEndToEndTestParams) expectStreamEvent(testEnv st.TestEnv, kind basictypes.StreamKind) eventsource.Event {
	stream, err := p.subscribeStream(testEnv, kind)
	require.Nil(p.t, err)
	require.NotNil(p.t, stream)
	defer stream.Close()
	return helpers.RequireValue(p.t, stream.Events, time.Second*5, "timed out waiting for stream event")
}

func (p relayEndToEndTestParams) expectStreamWithNoEvent(testEnv st.TestEnv, kind basictypes.StreamKind) {
	stream, err := p.subscribeStream(testEnv, kind)
	require.Nil(p.t, err)
	require.NotNil(p.t, stream)
	defer stream.Close()
	if !helpers.AssertNoMoreValues(p.t, stream.Events, time.Millisecond*100, "received unexpected stream event") {
		p.t.FailNow()
	}
}

func (p relayEndToEndTestParams) expectStreamError(testEnv st.TestEnv, kind basictypes.StreamKind, status int) {
	_, err := p.subscribeStream(testEnv, kind)
	require.NotNil(p.t, err)
	assert.Equal(p.t, status, err.Code)
}

func (p relayEndToEndTestParams) expectEvalResult(testEnv st.TestEnv, kind basictypes.SDKKind) ldvalue.Value {
	req := st.MakeSDKEvalEndpointRequest(p.relayURL, kind, testEnv, st.SimpleUserJSON, 0)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(p.t, err)
	defer resp.Body.Close()
	require.Equal(p.t, 200, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(p.t, err)
	return ldvalue.Parse(body)
}

func (p relayEndToEndTestParams) expectEvalError(testEnv st.TestEnv, kind basictypes.SDKKind, status int) {
	req := st.MakeSDKEvalEndpointRequest(p.relayURL, kind, testEnv, st.SimpleUserJSON, 0)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(p.t, err)
	require.Equal(p.t, status, resp.StatusCode)
}

func (p relayEndToEndTestParams) expectSuccessFromAllEndpoints(testEnv st.TestEnv) {
	serverSideEvent := p.expectStreamEvent(testEnv, basictypes.ServerSideStream)
	assert.Equal(p.t, "put", serverSideEvent.Event())
	jsonData := ldvalue.Parse([]byte(serverSideEvent.Data()))
	assert.Equal(p.t, []string{testFlag.Key}, jsonData.GetByKey("data").GetByKey("flags").Keys(nil))

	serverSideFlagsEvent := p.expectStreamEvent(testEnv, basictypes.ServerSideFlagsOnlyStream)
	assert.Equal(p.t, "put", serverSideFlagsEvent.Event())
	jsonFlagsData := ldvalue.Parse([]byte(serverSideFlagsEvent.Data()))
	assert.Equal(p.t, []string{testFlag.Key}, jsonFlagsData.Keys(nil))

	mobileEvent := p.expectStreamEvent(testEnv, basictypes.MobilePingStream)
	assert.Equal(p.t, "ping", mobileEvent.Event())

	mobileEval := p.expectEvalResult(testEnv, basictypes.MobileSDK)
	assert.Equal(p.t, []string{testFlag.Key}, mobileEval.Keys(nil))

	jsClientEval := p.expectEvalResult(testEnv, basictypes.JSClientSDK)
	assert.Equal(p.t, []string{testFlag.Key}, jsClientEval.Keys(nil))
}

func TestRelayEndToEndSuccess(t *testing.T) {
	putEvent := ldservices.NewServerSDKData().Flags(&testFlag).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv)}
	relayEndToEndTest(t, config, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
		p.waitForSuccessfulInit()
		p.expectSuccessFromAllEndpoints(testEnv)
	})
}

func TestRelayEndToEndPermanentFailure(t *testing.T) {
	streamHandler := httphelpers.HandlerWithStatus(401)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv)}
	behavior := relayTestBehavior{skipWaitForEnvironments: true}
	relayEndToEndTest(t, config, behavior, streamHandler, func(p relayEndToEndTestParams) {
		p.waitForLogMessage(ldlog.Error, "Error initializing LaunchDarkly client for", "initialization failure")
		p.expectStreamError(testEnv, basictypes.ServerSideStream, 401)
		p.expectStreamError(testEnv, basictypes.ServerSideFlagsOnlyStream, 401)
		p.expectStreamError(testEnv, basictypes.MobilePingStream, 401)
		p.expectStreamError(testEnv, basictypes.JSClientPingStream, 404)
		p.expectEvalError(testEnv, basictypes.MobileSDK, 401)
		p.expectEvalError(testEnv, basictypes.JSClientSDK, 404)
	})
}

func TestRelayCoreEndToEndInitTimeoutWithUninitializedDataStore(t *testing.T) {
	// For the "timed out, but we *do* have previous flag data" condition, see
	// TestRelayCoreEndToEndRedisInitTimeoutWithInitializedDataStore.
	putEvent := ldservices.NewServerSDKData().Flags(&testFlag).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	delayBeforeStreamHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-time.After(time.Second * 10):
			break
		case <-req.Context().Done(): // so the test won't be delayed when closing the test server
			break
		}
		streamHandler.ServeHTTP(w, req)
	})
	testEnv := st.EnvWithAllCredentials

	config := c.Config{
		Main: c.MainConfig{
			InitTimeout: configtypes.NewOptDuration(time.Millisecond),
		},
		Environment: st.MakeEnvConfigs(testEnv),
	}
	behavior := relayTestBehavior{skipWaitForEnvironments: true}
	relayEndToEndTest(t, config, behavior, delayBeforeStreamHandler, func(p relayEndToEndTestParams) {
		p.waitForLogMessage(ldlog.Error, "timeout encountered waiting for LaunchDarkly client initialization",
			"initialization timeout")
		p.expectStreamWithNoEvent(testEnv, basictypes.ServerSideStream)
		p.expectStreamWithNoEvent(testEnv, basictypes.ServerSideFlagsOnlyStream)
		p.expectStreamWithNoEvent(testEnv, basictypes.MobilePingStream)
		p.expectStreamWithNoEvent(testEnv, basictypes.JSClientPingStream)
		p.expectEvalError(testEnv, basictypes.MobileSDK, 503)
		p.expectEvalError(testEnv, basictypes.JSClientSDK, 503)
	})
}
