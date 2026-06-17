// Package testharness provides reusable integration test infrastructure for Phase 1
// concurrent-key testing. It has three components:
//
//   - RACMock: an SSE server that emits put/patch/delete events in the RAC protocol format.
//   - SDKSimulator: simulates a downstream SDK connecting to relay and consuming an SSE stream.
//   - ArchiveFixtureBuilder: builds offline-mode archive files (.tar.gz) for the filedata path.
//
// See README.md for usage examples.
package testharness

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
)

// RACMock is a test HTTP server that emits SSE events in the Relay Auto Config protocol
// format. Tests use it as relay's config-stream endpoint by pointing Main.StreamURI at
// its URL.
type RACMock struct {
	// URL is the server's base URL. Point relay's Main.StreamURI here.
	URL    string
	server *httptest.Server
	stream httphelpers.SSEStreamControl
}

// NewRACMock creates a new RACMock SSE server. If initialEvent is non-nil it is replayed
// to every new client that connects (use this for the initial put event). Cleanup is
// registered with t.Cleanup; call Close() explicitly only if you need early teardown.
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

// Enqueue queues an event to be delivered to the next client that connects. Use this
// before relay has connected to ensure the event is not dropped.
func (m *RACMock) Enqueue(event httphelpers.SSEEvent) {
	m.stream.Enqueue(event)
}

// Send emits an event to all currently connected clients. Use this after relay has
// connected and you want to trigger a live config update.
func (m *RACMock) Send(event httphelpers.SSEEvent) {
	m.stream.Send(event)
}

// Close shuts down the mock server and terminates any open SSE connections.
func (m *RACMock) Close() {
	_ = m.stream.Close()
	m.server.Close()
}

// MakePutEvent creates an SSE event representing a full RAC put, containing all of the
// given environments.
func MakePutEvent(envs ...envfactory.EnvironmentRep) httphelpers.SSEEvent {
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

// MakePatchEvent creates an SSE event representing a RAC patch for a single environment.
func MakePatchEvent(env envfactory.EnvironmentRep) httphelpers.SSEEvent {
	repBytes, _ := json.Marshal(env)
	msgBytes, _ := json.Marshal(autoconfig.PatchMessageData{
		Path: "/environments/" + string(env.EnvID),
		Data: repBytes,
	})
	return httphelpers.SSEEvent{Event: autoconfig.PatchEvent, Data: string(msgBytes)}
}

// MakeDeleteEvent creates an SSE event representing a RAC delete for an environment.
// version must be greater than the last-known version of that environment.
func MakeDeleteEvent(envID config.EnvironmentID, version int) httphelpers.SSEEvent {
	msgBytes, _ := json.Marshal(autoconfig.DeleteMessageData{
		Path:    "/environments/" + string(envID),
		Version: version,
	})
	return httphelpers.SSEEvent{Event: autoconfig.DeleteEvent, Data: string(msgBytes)}
}
