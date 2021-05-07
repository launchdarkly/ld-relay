package core

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/launchdarkly/eventsource"
	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/go-test-helpers/v2/ldservices"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests validate the processing of requests through a full HTTP request-response cycle, using a
// real embedded HTTP server to host the Relay instance, but without actually connecting to LaunchDarkly.

var testFlag = ldbuilders.NewFlagBuilder("test-flag").Version(1).
	ClientSideUsingMobileKey(true).ClientSideUsingEnvironmentID(true). // ensures that it'll show up in all endpoints
	Build()

type relayCoreEndToEndTestParams struct {
	t            *testing.T
	requestsCh   <-chan httphelpers.HTTPRequestInfo
	relayURL     string
	relayMockLog *ldlogtest.MockLog
	loggers      ldlog.Loggers
	singleLogger ldlog.BaseLogger
}

func relayCoreEndToEndTest(t *testing.T, config c.Config, ldStreamHandler http.Handler, action func(p relayCoreEndToEndTestParams)) {
	relayMockLog := ldlogtest.NewMockLog()
	defer relayMockLog.DumpIfTestFailed(t)

	streamHandler, requestsCh := httphelpers.RecordingHandler(ldStreamHandler)
	eventsHandler := httphelpers.HandlerWithStatus(202)

	httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
		httphelpers.WithServer(eventsHandler, func(eventsServer *httptest.Server) {
			config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
			config.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(eventsServer.URL)
			core, err := NewRelayCore(
				config,
				relayMockLog.Loggers,
				nil,
				"1.2.3",
				"FakeRelay",
				relayenv.LogNameIsEnvID,
			)
			require.NoError(t, err)
			defer core.Close()

			for _, env := range config.Environment {
				streamReq := st.ExpectTestRequest(t, requestsCh, time.Second*5)
				assert.Equal(t, string(env.SDKKey), streamReq.Request.Header.Get("Authorization"))
			}

			httphelpers.WithServer(core.MakeRouter(), func(relayServer *httptest.Server) {
				mockLog := ldlogtest.NewMockLog()
				mockLog.Loggers.SetPrefix("TestClient:")
				defer mockLog.DumpIfTestFailed(t)
				p := relayCoreEndToEndTestParams{
					t:            t,
					requestsCh:   requestsCh,
					relayURL:     relayServer.URL,
					relayMockLog: relayMockLog,
					loggers:      mockLog.Loggers,
					singleLogger: mockLog.Loggers.ForLevel(ldlog.Info),
				}
				action(p)
			})
		})
	})
}

func (p relayCoreEndToEndTestParams) waitForSuccessfulInit() {
	p.waitForLogMessage(ldlog.Info, "Initialized LaunchDarkly client for", "Relay initialization")
}

func (p relayCoreEndToEndTestParams) waitForLogMessage(level ldlog.LogLevel, pattern, conditionDesc string) {
	require.Eventually(p.t, func() bool {
		return p.relayMockLog.HasMessageMatch(level, pattern)
	}, time.Second*2, time.Millisecond*50, "timed out waiting for %s", conditionDesc)
}

func (p relayCoreEndToEndTestParams) subscribeStream(testEnv st.TestEnv, kind basictypes.StreamKind) (
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

func (p relayCoreEndToEndTestParams) expectStreamEvent(testEnv st.TestEnv, kind basictypes.StreamKind) eventsource.Event {
	stream, err := p.subscribeStream(testEnv, kind)
	require.Nil(p.t, err)
	require.NotNil(p.t, stream)
	defer stream.Close()
	return st.ExpectStreamEvent(p.t, stream, time.Second*5)
}

func (p relayCoreEndToEndTestParams) expectStreamWithNoEvent(testEnv st.TestEnv, kind basictypes.StreamKind) {
	stream, err := p.subscribeStream(testEnv, kind)
	require.Nil(p.t, err)
	require.NotNil(p.t, stream)
	defer stream.Close()
	st.ExpectNoStreamEvent(p.t, stream, time.Millisecond*100)
}

func (p relayCoreEndToEndTestParams) expectStreamError(testEnv st.TestEnv, kind basictypes.StreamKind, status int) {
	_, err := p.subscribeStream(testEnv, kind)
	require.NotNil(p.t, err)
	assert.Equal(p.t, status, err.Code)
}

func (p relayCoreEndToEndTestParams) expectEvalResult(testEnv st.TestEnv, kind basictypes.SDKKind) ldvalue.Value {
	req := st.MakeSDKEvalEndpointRequest(p.relayURL, kind, testEnv, st.SimpleUserJSON, 0)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(p.t, err)
	defer resp.Body.Close()
	require.Equal(p.t, 200, resp.StatusCode)
	body, err := ioutil.ReadAll(resp.Body)
	require.NoError(p.t, err)
	return ldvalue.Parse(body)
}

func (p relayCoreEndToEndTestParams) expectEvalError(testEnv st.TestEnv, kind basictypes.SDKKind, status int) {
	req := st.MakeSDKEvalEndpointRequest(p.relayURL, kind, testEnv, st.SimpleUserJSON, 0)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(p.t, err)
	require.Equal(p.t, status, resp.StatusCode)
}

func (p relayCoreEndToEndTestParams) expectSuccessFromAllEndpoints(testEnv st.TestEnv) {
	serverSideEvent := p.expectStreamEvent(testEnv, basictypes.ServerSideStream)
	assert.Equal(p.t, "put", serverSideEvent.Event())
	jsonData := ldvalue.Parse([]byte(serverSideEvent.Data()))
	assert.Equal(p.t, []string{testFlag.Key}, jsonData.GetByKey("data").GetByKey("flags").Keys())

	serverSideFlagsEvent := p.expectStreamEvent(testEnv, basictypes.ServerSideFlagsOnlyStream)
	assert.Equal(p.t, "put", serverSideFlagsEvent.Event())
	jsonFlagsData := ldvalue.Parse([]byte(serverSideFlagsEvent.Data()))
	assert.Equal(p.t, []string{testFlag.Key}, jsonFlagsData.Keys())

	mobileEvent := p.expectStreamEvent(testEnv, basictypes.MobilePingStream)
	assert.Equal(p.t, "ping", mobileEvent.Event())

	mobileEval := p.expectEvalResult(testEnv, basictypes.MobileSDK)
	assert.Equal(p.t, []string{testFlag.Key}, mobileEval.Keys())

	jsClientEval := p.expectEvalResult(testEnv, basictypes.JSClientSDK)
	assert.Equal(p.t, []string{testFlag.Key}, jsClientEval.Keys())
}

func TestRelayCoreEndToEndSuccess(t *testing.T) {
	putEvent := ldservices.NewServerSDKData().Flags(&testFlag).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv)}
	relayCoreEndToEndTest(t, config, streamHandler, func(p relayCoreEndToEndTestParams) {
		p.waitForSuccessfulInit()
		p.expectSuccessFromAllEndpoints(testEnv)
	})
}

func TestRelayCoreEndToEndPermanentFailure(t *testing.T) {
	streamHandler := httphelpers.HandlerWithStatus(401)
	testEnv := st.EnvWithAllCredentials

	config := c.Config{Environment: st.MakeEnvConfigs(testEnv)}
	relayCoreEndToEndTest(t, config, streamHandler, func(p relayCoreEndToEndTestParams) {
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
	relayCoreEndToEndTest(t, config, delayBeforeStreamHandler, func(p relayCoreEndToEndTestParams) {
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
