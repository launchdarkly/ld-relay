package wire

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeHelloFillsTypeAndVersion(t *testing.T) {
	frame, err := EncodeHello(Hello{
		Capabilities: []string{CapabilityBatchRefs, CapabilityZstd},
		Root:         "sdk-env",
		Scopes:       []HelloScope{{Key: "sdk-env"}, {Key: "sdk-viewA", State: "sel-1"}},
		InstanceID:   "instance-1",
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(frame, &decoded))
	assert.Equal(t, "hello", decoded["type"])
	assert.Equal(t, float64(1), decoded["version"])
	assert.Equal(t, "sdk-env", decoded["root"])
	assert.Equal(t, "instance-1", decoded["instanceId"])
}

func TestEncodeHelloDoesNotMutateCaller(t *testing.T) {
	hello := Hello{Root: "sdk-env", Scopes: []HelloScope{{Key: "sdk-env"}}}
	_, err := EncodeHello(hello)
	require.NoError(t, err)
	assert.Empty(t, hello.Type)
	assert.Zero(t, hello.Version)
}

func TestEncodeHelloOmitsUnsetOptionalFields(t *testing.T) {
	frame, err := EncodeHello(Hello{Root: "sdk-env", Scopes: []HelloScope{{Key: "sdk-env"}}})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(frame, &decoded))
	assert.NotContains(t, decoded, "relayVersion")
	assert.NotContains(t, decoded, "instanceId")

	// A scope with no recorded state must omit the field rather than present an empty
	// selector, which the server would have to treat as a state to resume from.
	scopes, ok := decoded["scopes"].([]any)
	require.True(t, ok)
	require.Len(t, scopes, 1)
	assert.NotContains(t, scopes[0], "state")
}

// The handshake deliberately spells the field two ways: Welcome uses "type", while Goodbye
// and ErrorMessage keep the envelope's "t" because they travel both framings.
func TestDecodeHandshakeReadsBothTypeFieldSpellings(t *testing.T) {
	welcome, err := DecodeHandshake([]byte(
		`{"type":"welcome","version":1,"capabilities":["msgpack","batch-refs"],` +
			`"heartbeatIntervalMs":15000,"base":"payload-1","maxScopes":50}`))
	require.NoError(t, err)
	require.IsType(t, Welcome{}, welcome)
	assert.Equal(t, Welcome{
		Type:                "welcome",
		Version:             1,
		Capabilities:        []string{CapabilityMsgpack, CapabilityBatchRefs},
		HeartbeatIntervalMs: 15000,
		Base:                "payload-1",
		MaxScopes:           50,
	}, welcome)

	goodbye, err := DecodeHandshake([]byte(
		`{"t":"goodbye","reason":"the root credential is view-scoped",` +
			`"code":"root-not-environment-wide"}`))
	require.NoError(t, err)
	assert.Equal(t, Goodbye{
		T:      "goodbye",
		Reason: "the root credential is view-scoped",
		Code:   GoodbyeRootNotEnvironmentWide,
	}, goodbye)

	scopeError, err := DecodeHandshake([]byte(
		`{"t":"error","scope":"rel-autoconfig","code":"invalid-key","message":"not a scope"}`))
	require.NoError(t, err)
	assert.Equal(t, ErrorMessage{
		T:       "error",
		Scope:   "rel-autoconfig",
		Code:    ErrorInvalidKey,
		Message: "not a scope",
	}, scopeError)
}

// An absent maxScopes means the server advertised no bound. The schema forbids a real bound
// of zero, so the zero value is unambiguous.
func TestDecodeHandshakeAbsentMaxScopesIsZero(t *testing.T) {
	welcome, err := DecodeHandshake([]byte(
		`{"type":"welcome","version":1,"capabilities":["msgpack"],` +
			`"heartbeatIntervalMs":15000,"base":"payload-1"}`))
	require.NoError(t, err)
	assert.Zero(t, welcome.(Welcome).MaxScopes)
}

func TestDecodeHandshakeRejectsUnusableFrames(t *testing.T) {
	for name, frame := range map[string]string{
		"malformed json":     `{"type":`,
		"no message type":    `{"version":1}`,
		"unexpected message": `{"t":"heartbeat"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeHandshake([]byte(frame))
			assert.Error(t, err)
		})
	}
}
