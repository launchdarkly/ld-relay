package configsource

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
)

// RACMock is a test HTTP server that emits SSE events in the Relay Auto Config protocol format.
// Tests use it as Relay's config-stream endpoint by pointing Main.StreamURI at its URL.
//
// It is a thin, reusable wrapper over httphelpers.SSEHandler + httptest.Server with automatic
// cleanup, intended for tests outside package relay that cannot reach that package's unexported
// autoConfTest helper. Use the MakeAutoConfig*Event builders to construct events.
type RACMock struct {
	// URL is the server's base URL. Point Relay's Main.StreamURI here.
	URL    string
	server *httptest.Server
	stream httphelpers.SSEStreamControl
}

// NewRACMock creates a new RACMock SSE server. If initialEvent is non-nil it is replayed to every
// new client that connects (use this for the initial put event). Cleanup is registered with
// t.Cleanup; call Close() explicitly only if you need early teardown.
func NewRACMock(t testing.TB, initialEvent *httphelpers.SSEEvent) *RACMock {
	handler, stream := httphelpers.SSEHandler(initialEvent)
	server := httptest.NewServer(handler)
	m := &RACMock{
		URL:    server.URL,
		server: server,
		stream: stream,
	}
	t.Cleanup(m.Close)
	return m
}

// NewRACMockWithReconnect creates a RACMock that serves firstEvent to the first client that connects
// and reconnectEvent to the next client, modeling a stream that a client restarts and reconnects to.
// This supports malformed-payload recovery: a rejected patch forces Relay to restart
// its config stream, and the backend serves a fresh, corrected put on the reconnection.
//
// Send delivers to the first connection; use it to push the event that forces the restart (e.g. the
// malformed patch). initialEvent replay applies per handler, so the first connection sees firstEvent
// and the reconnection sees reconnectEvent. Cleanup is registered with t.Cleanup.
func NewRACMockWithReconnect(t testing.TB, firstEvent, reconnectEvent *httphelpers.SSEEvent) *RACMock {
	firstHandler, firstStream := httphelpers.SSEHandler(firstEvent)
	reconnectHandler, _ := httphelpers.SSEHandler(reconnectEvent)
	handler := httphelpers.SequentialHandler(firstHandler, reconnectHandler)
	server := httptest.NewServer(handler)
	m := &RACMock{
		URL:    server.URL,
		server: server,
		stream: firstStream,
	}
	t.Cleanup(m.Close)
	return m
}

// Enqueue queues an event to be delivered to the next client that connects. Use this before Relay
// has connected to ensure the event is not dropped.
func (m *RACMock) Enqueue(event httphelpers.SSEEvent) {
	m.stream.Enqueue(event)
}

// Send emits an event to all currently connected clients. Use this after Relay has connected and
// you want to trigger a live config update.
func (m *RACMock) Send(event httphelpers.SSEEvent) {
	m.stream.Send(event)
}

// Close shuts down the mock server and terminates any open SSE connections.
func (m *RACMock) Close() {
	_ = m.stream.Close()
	m.server.Close()
}

// MakeAutoConfigPutEvent creates an SSE event representing a full RAC put, containing all of the
// given environments.
func MakeAutoConfigPutEvent(envs ...envfactory.EnvironmentRep) httphelpers.SSEEvent {
	data := autoconfig.PutMessageData{
		Path: "/",
		Data: autoconfig.PutContent{
			Environments: make(map[config.EnvironmentID]envfactory.EnvironmentRep, len(envs)),
		},
	}
	for _, e := range envs {
		data.Data.Environments[e.EnvID] = e
	}
	jsonBytes, _ := json.Marshal(data)
	return httphelpers.SSEEvent{Event: autoconfig.PutEvent, Data: string(jsonBytes)}
}

// MakeAutoConfigPatchEvent creates an SSE event representing a RAC patch for a single environment.
func MakeAutoConfigPatchEvent(env envfactory.EnvironmentRep) httphelpers.SSEEvent {
	repBytes, _ := json.Marshal(env)
	msgBytes, _ := json.Marshal(autoconfig.PatchMessageData{
		Path: "/environments/" + string(env.EnvID),
		Data: repBytes,
	})
	return httphelpers.SSEEvent{Event: autoconfig.PatchEvent, Data: string(msgBytes)}
}

// MakeAutoConfigDeleteEvent creates an SSE event representing a RAC delete for an environment.
// version must be greater than the last-known version of that environment.
func MakeAutoConfigDeleteEvent(envID config.EnvironmentID, version int) httphelpers.SSEEvent {
	msgBytes, _ := json.Marshal(autoconfig.DeleteMessageData{
		Path:    "/environments/" + string(envID),
		Version: version,
	})
	return httphelpers.SSEEvent{Event: autoconfig.DeleteEvent, Data: string(msgBytes)}
}
