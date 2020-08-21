package autoconfig

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay-core/httpconfig"
	st "github.com/launchdarkly/ld-relay-core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/enterprise/entconfig"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"

	"github.com/stretchr/testify/require"
)

const (
	testConfigKey entconfig.AutoConfigKey = "test-key"
	testEnvName                           = "projname envname"
	malformedJSON                         = `{"oh no`
)

var (
	testEnv1 = EnvironmentRep{
		EnvID:      config.EnvironmentID("envid1"),
		EnvKey:     "envkey1",
		EnvName:    "envname1",
		MobKey:     config.MobileKey("mobkey1"),
		ProjKey:    "projkey1",
		ProjName:   "projname1",
		SDKKey:     SDKKeyRep{Value: config.SDKKey("sdkkey1")},
		DefaultTTL: 2,
		SecureMode: true,
		Version:    10,
	}
	testEnv2 = EnvironmentRep{
		EnvID:    config.EnvironmentID("envid2"),
		EnvKey:   "envkey2",
		EnvName:  "envname2",
		MobKey:   config.MobileKey("mobkey2"),
		ProjKey:  "projkey2",
		ProjName: "projname2",
		SDKKey:   SDKKeyRep{Value: config.SDKKey("sdkkey2")},
		Version:  20,
	}
	emptyPutMessage = httphelpers.SSEEvent{Event: PutEvent, Data: `{"path": "/", "data": {"environments": {}}}`}
)

func toJSON(x interface{}) string {
	bytes, _ := json.Marshal(x)
	return string(bytes)
}

func makePutEvent(envs ...EnvironmentRep) httphelpers.SSEEvent {
	m := make(map[string]EnvironmentRep)
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

func makePatchEvent(env EnvironmentRep) httphelpers.SSEEvent {
	return httphelpers.SSEEvent{
		Event: PatchEvent,
		Data: toJSON(map[string]interface{}{
			"path": "/environments/" + string(env.EnvID),
			"data": env,
		}),
	}
}

func makeDeleteEvent(envID config.EnvironmentID, version int) httphelpers.SSEEvent {
	return httphelpers.SSEEvent{
		Event: DeleteEvent,
		Data: toJSON(map[string]interface{}{
			"path":    "/environments/" + string(envID),
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
	add     *EnvironmentParams
	update  *EnvironmentParams
	delete  *config.EnvironmentID
	expired *expiredKey
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
	if m.expired != nil {
		return fmt.Sprintf("expired(%+v)", *m.expired)
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

func streamManagerTestWithStreamHandler(
	t *testing.T,
	streamHandler http.Handler,
	stream httphelpers.SSEStreamControl,
	action func(p streamManagerTestParams),
) {
	mockLog := ldlogtest.NewMockLog()
	defer st.DumpLogIfTestFailed(t, mockLog)

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
			server.URL,
			testMessageHandler,
			httpConfig,
			time.Millisecond,
			mockLog.Loggers,
		)
		defer p.streamManager.Close()

		action(p)
	})
}

func (p streamManagerTestParams) startStream() {
	readyCh := p.streamManager.Start()
	select {
	case <-readyCh:
		break
	case <-time.After(time.Second):
		require.Fail(p.t, "timed out waiting for stream ready")
	}
}

func (p streamManagerTestParams) requireMessage() testMessage {
	select {
	case m := <-p.messageHandler.received:
		return m
	case <-time.After(500 * time.Millisecond):
		require.Fail(p.t, "timed out waiting for message")
		return testMessage{}
	}
}

func (p streamManagerTestParams) requireNoMoreMessages() {
	select {
	case m := <-p.messageHandler.received:
		require.Failf(p.t, "received unexpected message", "%s", m)
	case <-time.After(50 * time.Millisecond):
		break
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

func (h *testMessageHandler) AddEnvironment(params EnvironmentParams) {
	h.received <- testMessage{add: &params}
}

func (h *testMessageHandler) UpdateEnvironment(params EnvironmentParams) {
	h.received <- testMessage{update: &params}
}

func (h *testMessageHandler) DeleteEnvironment(id config.EnvironmentID) {
	h.received <- testMessage{delete: &id}
}

func (h *testMessageHandler) KeyExpired(envID config.EnvironmentID, key config.SDKKey) {
	h.received <- testMessage{expired: &expiredKey{envID, key}}
}
