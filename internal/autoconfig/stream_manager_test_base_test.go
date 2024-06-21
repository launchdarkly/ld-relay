package autoconfig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/require"
)

const (
	testConfigKey config.AutoConfigKey = "test-key"
	testEnvName                        = "projname envname"
	malformedJSON                      = `{"oh no`

	rpacProtocolVersion = 2
)

var (
	testEnv1 = envfactory.EnvironmentRep{
		EnvID:      config.EnvironmentID("envid1"),
		EnvKey:     "envkey1",
		EnvName:    "envname1",
		MobKey:     config.MobileKey("mobkey1"),
		ProjKey:    "projkey1",
		ProjName:   "projname1",
		SDKKey:     envfactory.SDKKeyRep{Value: config.SDKKey("sdkkey1")},
		DefaultTTL: 2,
		SecureMode: true,
		Version:    10,
	}
	testEnv2 = envfactory.EnvironmentRep{
		EnvID:    config.EnvironmentID("envid2"),
		EnvKey:   "envkey2",
		EnvName:  "envname2",
		MobKey:   config.MobileKey("mobkey2"),
		ProjKey:  "projkey2",
		ProjName: "projname2",
		SDKKey:   envfactory.SDKKeyRep{Value: config.SDKKey("sdkkey2")},
		Version:  20,
	}
	testFilter1 = envfactory.FilterRep{
		ProjKey:   "projkey1",
		FilterKey: "filterkey1",
		Version:   10,
	}
	testFilter2 = envfactory.FilterRep{
		ProjKey:   "projkey2",
		FilterKey: "filterkey1",
		Version:   20,
	}
	emptyPutMessage = httphelpers.SSEEvent{Event: PutEvent, Data: `{"path": "/", "data": {"environments": {}}}`}
)

func toJSON(x interface{}) string {
	bytes, _ := json.Marshal(x)
	return string(bytes)
}

func makeEnvPutEvent(envs ...envfactory.EnvironmentRep) httphelpers.SSEEvent {
	m := make(map[string]envfactory.EnvironmentRep)
	for _, e := range envs {
		m[string(e.EnvID)] = e
	}
	return httphelpers.SSEEvent{
		Event: PutEvent,
		Data: toJSON(map[string]interface{}{
			"path": "/",
			"data": map[string]interface{}{"environments": m},
		}),
	}
}

func filterID(rep envfactory.FilterRep) config.FilterID {
	return config.FilterID(fmt.Sprintf("%s.%s", rep.ProjKey, rep.FilterKey))
}

func makeEnvFilterPutEvent(envs []envfactory.EnvironmentRep, filters []envfactory.FilterRep) httphelpers.SSEEvent {
	envMap := make(map[config.EnvironmentID]envfactory.EnvironmentRep)
	filterMap := make(map[config.FilterID]envfactory.FilterRep)
	for _, e := range envs {
		envMap[e.EnvID] = e
	}
	for _, f := range filters {
		filterMap[filterID(f)] = f
	}
	return httphelpers.SSEEvent{
		Event: PutEvent,
		Data: toJSON(map[string]interface{}{
			"path": "/",
			"data": map[string]interface{}{"environments": envMap, "filters": filterMap},
		}),
	}
}

func makeFilterPutEvent(filters ...envfactory.FilterRep) httphelpers.SSEEvent {
	filterMap := make(map[config.FilterID]envfactory.FilterRep)
	for _, f := range filters {
		filterMap[filterID(f)] = f
	}
	return httphelpers.SSEEvent{
		Event: PutEvent,
		Data: toJSON(map[string]interface{}{
			"path": "/",
			"data": map[string]interface{}{"filters": filterMap},
		}),
	}
}

func makePatchEnvEvent(env envfactory.EnvironmentRep) httphelpers.SSEEvent {
	return httphelpers.SSEEvent{
		Event: PatchEvent,
		Data: toJSON(map[string]interface{}{
			"path": "/environments/" + string(env.EnvID),
			"data": env,
		}),
	}
}

func makePatchFilterEvent(f envfactory.FilterRep) httphelpers.SSEEvent {
	return httphelpers.SSEEvent{
		Event: PatchEvent,
		Data: toJSON(map[string]interface{}{
			"path": "/filters/" + fmt.Sprintf("%s.%s", f.ProjKey, f.FilterKey),
			"data": f,
		}),
	}
}

func makeDeleteEnvEvent(envID config.EnvironmentID, version int) httphelpers.SSEEvent {
	return httphelpers.SSEEvent{
		Event: DeleteEvent,
		Data: toJSON(map[string]interface{}{
			"path":    "/environments/" + string(envID),
			"version": version,
		}),
	}
}

func makeDeleteFilterEvent(filterID config.FilterID, version int) httphelpers.SSEEvent {
	return httphelpers.SSEEvent{
		Event: DeleteEvent,
		Data: toJSON(map[string]interface{}{
			"path":    "/filters/" + string(filterID),
			"version": version,
		}),
	}
}

type streamManagerTestParams struct {
	t              *testing.T
	streamManager  *StreamManager
	messageHandler *testMessageHandler
	stream         httphelpers.SSEStreamControl
	requestsCh     <-chan httphelpers.HTTPRequestInfo
	mockLog        *ldlogtest.MockLog
}

type testMessage struct {
	add          *envfactory.EnvironmentParams
	addFilter    *envfactory.FilterParams
	update       *envfactory.EnvironmentParams
	delete       *config.EnvironmentID
	deleteFilter *config.FilterID
	receivedAll  bool
}

func (m testMessage) String() string {
	if m.add != nil {
		return fmt.Sprintf("add(%+v)", *m.add)
	}
	if m.update != nil {
		return fmt.Sprintf("update(%+v)", *m.update)
	}
	if m.delete != nil {
		return fmt.Sprintf("delete(%+v)", *m.delete)
	}
	if m.receivedAll {
		return "receivedAllEnvironments"
	}
	return "???"
}

type testMessageHandler struct {
	received chan testMessage
}

func streamManagerTest(t *testing.T, initialEvent *httphelpers.SSEEvent, action func(p streamManagerTestParams)) {
	streamHandler, stream := httphelpers.SSEHandler(initialEvent)
	defer stream.Close()
	streamManagerTestWithStreamHandler(t, streamHandler, stream, action)
}

func mustParseURL(t *testing.T, u string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func streamManagerTestWithStreamHandler(
	t *testing.T,
	streamHandler http.Handler,
	stream httphelpers.SSEStreamControl,
	action func(p streamManagerTestParams),
) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	handler, requestsCh := httphelpers.RecordingHandler(autoConfigEndpointHandler(streamHandler))
	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", mockLog.Loggers)
	if err != nil {
		panic(err)
	}
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		testMessageHandler := newTestMessageHandler()
		p := streamManagerTestParams{
			t:              t,
			stream:         stream,
			messageHandler: testMessageHandler,
			requestsCh:     requestsCh,
			mockLog:        mockLog,
		}
		p.streamManager = NewStreamManager(
			testConfigKey,
			mustParseURL(t, server.URL),
			testMessageHandler,
			httpConfig,
			time.Millisecond,
			rpacProtocolVersion,
			mockLog.Loggers,
		)
		defer p.streamManager.Close()

		action(p)
	})
}

func (p streamManagerTestParams) startStream() {
	readyCh := p.streamManager.Start()
	helpers.RequireValue(p.t, readyCh, time.Second, "timed out waiting for stream ready")
}

func (p streamManagerTestParams) requireMessage() testMessage {
	return helpers.RequireValue(p.t, p.messageHandler.received, 500*time.Millisecond, "timed out waiting for message")
}

func (p streamManagerTestParams) requireReceivedAllMessage() {
	m := p.requireMessage()
	require.Equal(p.t, testMessage{receivedAll: true}, m)
}

func (p streamManagerTestParams) requireNoMoreMessages() {
	if !helpers.AssertNoMoreValues(p.t, p.messageHandler.received, 50*time.Millisecond, "received unexpected message") {
		p.t.FailNow()
	}
}

func autoConfigEndpointHandler(streamHandler http.Handler) http.Handler {
	return httphelpers.HandlerForPath(autoConfigStreamPath, httphelpers.HandlerForMethod("GET", streamHandler, nil), nil)
}

func newTestMessageHandler() *testMessageHandler {
	return &testMessageHandler{
		received: make(chan testMessage, 10),
	}
}

func (h *testMessageHandler) AddEnvironment(params envfactory.EnvironmentParams) {
	h.received <- testMessage{add: &params}
}

func (h *testMessageHandler) UpdateEnvironment(params envfactory.EnvironmentParams) {
	h.received <- testMessage{update: &params}
}

func (h *testMessageHandler) DeleteEnvironment(id config.EnvironmentID) {
	h.received <- testMessage{delete: &id}
}

func (h *testMessageHandler) ReceivedAllEnvironments() {
	h.received <- testMessage{receivedAll: true}
}

func (h *testMessageHandler) AddFilter(params envfactory.FilterParams) {
	h.received <- testMessage{addFilter: &params}
}

func (h *testMessageHandler) DeleteFilter(id config.FilterID) {
	h.received <- testMessage{deleteFilter: &id}
}
