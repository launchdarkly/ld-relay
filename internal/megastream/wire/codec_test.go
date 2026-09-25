package wire

import (
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"
)

func newTestCodec(t *testing.T, negotiated ...string) *Codec {
	t.Helper()
	codec, err := NewCodec(negotiated)
	require.NoError(t, err)
	t.Cleanup(codec.Close)
	return codec
}

// frame marshals a message the way the server would, with no compression prefix.
func frame(t *testing.T, msg any) []byte {
	t.Helper()
	body, err := msgpack.Marshal(msg)
	require.NoError(t, err)
	return body
}

func TestDecodeEachInboundMessage(t *testing.T) {
	codec := newTestCodec(t)
	flagJSON := []byte(`{"key":"f1","version":13,"on":true}`)

	for name, tc := range map[string]struct {
		sent any
		want any
	}{
		"server-intent": {
			sent: map[string]any{
				"t": "server-intent", "scope": "sdk-viewA",
				"intentCode": "xfer-full", "unfiltered": false,
			},
			want: ServerIntent{
				T: TypeServerIntent, Scope: "sdk-viewA",
				IntentCode: IntentTransferFull, Unfiltered: boolPtr(false),
			},
		},
		"server-intent unfiltered": {
			sent: map[string]any{
				"t": "server-intent", "scope": "sdk-env",
				"intentCode": "xfer-changes", "unfiltered": true, "reason": "resumed",
			},
			want: ServerIntent{
				T: TypeServerIntent, Scope: "sdk-env",
				IntentCode: IntentTransferChanges, Unfiltered: boolPtr(true), Reason: "resumed",
			},
		},
		"put-object": {
			sent: map[string]any{
				"t": "put-object", "base": "payload-1", "kind": "flag",
				"key": "f1", "version": 13, "object": flagJSON,
			},
			want: PutObject{
				T: TypePutObject, Base: "payload-1", Kind: "flag",
				Key: "f1", Version: 13, Object: flagJSON,
			},
		},
		"delete-object": {
			sent: map[string]any{
				"t": "delete-object", "base": "payload-1",
				"kind": "flag", "key": "f9", "version": 15,
			},
			want: DeleteObject{
				T: TypeDeleteObject, Base: "payload-1",
				Kind: "flag", Key: "f9", Version: 15,
			},
		},
		"payload-transferred": {
			sent: map[string]any{
				"t": "payload-transferred", "scope": "sdk-viewA",
				"state": "sel-7", "version": 15,
			},
			want: PayloadTransferred{
				T: TypePayloadTransferred, Scope: "sdk-viewA",
				State: "sel-7", Version: 15,
			},
		},
		"ref": {
			sent: map[string]any{"t": "ref", "scope": "sdk-viewA", "kind": "flag", "key": "f1"},
			want: Ref{T: TypeRef, Scope: "sdk-viewA", Kind: "flag", Key: "f1"},
		},
		"ref-delete": {
			sent: map[string]any{"t": "ref-delete", "scope": "sdk-viewA", "kind": "flag", "key": "f2"},
			want: RefDelete{T: TypeRefDelete, Scope: "sdk-viewA", Kind: "flag", Key: "f2"},
		},
		"ref-batch": {
			sent: RefBatch{
				T: TypeRefBatch, Scope: "sdk-viewB",
				Refs:    []ObjectRef{{Kind: "flag", Key: "f3"}},
				Deletes: []ObjectRef{{Kind: "segment", Key: "s2"}},
			},
			want: RefBatch{
				T: TypeRefBatch, Scope: "sdk-viewB",
				Refs:    []ObjectRef{{Kind: "flag", Key: "f3"}},
				Deletes: []ObjectRef{{Kind: "segment", Key: "s2"}},
			},
		},
		"error": {
			sent: map[string]any{
				"t": "error", "scope": "sdk-viewA",
				"code": "scope-revoked", "message": "revoked",
			},
			want: ErrorMessage{
				T: TypeError, Scope: "sdk-viewA",
				Code: ErrorScopeRevoked, Message: "revoked",
			},
		},
		"heartbeat": {
			sent: map[string]any{"t": "heartbeat"},
			want: Heartbeat{T: TypeHeartbeat},
		},
		"goodbye": {
			sent: map[string]any{
				"t": "goodbye", "reason": "deploying",
				"code": "draining", "backoffMs": 5000,
			},
			want: Goodbye{T: TypeGoodbye, Reason: "deploying", Code: GoodbyeDraining, BackoffMs: 5000},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := codec.Decode(frame(t, tc.sent))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The schema gives put-object.object no declared type. Its description asks for a MessagePack
// bin value, but that is prose rather than a requirement, so a decoder that accepts only one
// of the two is an interop failure waiting to happen.
func TestDecodePutObjectAcceptsBinAndStringContent(t *testing.T) {
	codec := newTestCodec(t)
	flagJSON := `{"key":"f1","version":13}`

	asBin := frame(t, map[string]any{
		"t": "put-object", "base": "payload-1", "kind": "flag",
		"key": "f1", "version": 13, "object": []byte(flagJSON),
	})
	asString := frame(t, map[string]any{
		"t": "put-object", "base": "payload-1", "kind": "flag",
		"key": "f1", "version": 13, "object": flagJSON,
	})
	require.NotEqual(t, asBin, asString, "the two encodings must actually differ on the wire")

	for name, encoded := range map[string][]byte{"bin": asBin, "str": asString} {
		t.Run(name, func(t *testing.T) {
			got, err := codec.Decode(encoded)
			require.NoError(t, err)
			assert.Equal(t, []byte(flagJSON), got.(PutObject).Object)
		})
	}
}

// Relay stores and forwards object content without re-encoding it, so the bytes that come out
// of the decoder have to be the bytes that went in, whitespace and key order included.
func TestDecodePutObjectPreservesContentVerbatim(t *testing.T) {
	codec := newTestCodec(t)
	original := []byte("{ \"version\":13,\n  \"key\" : \"f1\" }")

	got, err := codec.Decode(frame(t, map[string]any{
		"t": "put-object", "base": "payload-1", "kind": "flag",
		"key": "f1", "version": 13, "object": original,
	}))
	require.NoError(t, err)
	assert.Equal(t, original, got.(PutObject).Object)
}

// Object kinds are an open set. An unrecognized kind travels exactly as a known one, because
// relay is an intermediary and dropping it would starve a newer SDK behind an older relay.
func TestDecodePutObjectCarriesUnknownKinds(t *testing.T) {
	codec := newTestCodec(t)
	got, err := codec.Decode(frame(t, map[string]any{
		"t": "put-object", "base": "payload-1", "kind": "ai-config",
		"key": "c1", "version": 4, "object": []byte(`{"key":"c1"}`),
	}))
	require.NoError(t, err)
	assert.Equal(t, "ai-config", got.(PutObject).Kind)
}

// An unrecognized message type is ignored rather than fatal, so the codec reports it as a
// value the caller can log. Messages relay itself sends take the same path: a server has no
// reason to send one, and failing the connection over it would be worse than ignoring it.
func TestDecodeUnrecognizedTypesSurfaceAsUnknown(t *testing.T) {
	codec := newTestCodec(t)
	for _, messageType := range []string{"invented-later", TypeHeartbeatAck, TypeScopeAdd, TypeScopeRemove} {
		got, err := codec.Decode(frame(t, map[string]any{"t": messageType}))
		require.NoError(t, err, messageType)
		assert.Equal(t, Unknown{T: messageType}, got)
	}
}

// Unrecognized envelope fields are ignored. That rule is what makes capability-gated
// additions safe to deploy against relays that predate them.
func TestDecodeIgnoresUnrecognizedFields(t *testing.T) {
	codec := newTestCodec(t)
	got, err := codec.Decode(frame(t, map[string]any{
		"t": "ref", "scope": "sdk-viewA", "kind": "flag", "key": "f1",
		"somethingAddedLater": []string{"a", "b"},
	}))
	require.NoError(t, err)
	assert.Equal(t, Ref{T: TypeRef, Scope: "sdk-viewA", Kind: "flag", Key: "f1"}, got)
}

func TestDecodeRejectsUndecodableFrames(t *testing.T) {
	codec := newTestCodec(t)
	_, err := codec.Decode([]byte{0xc1, 0x00, 0x42})
	assert.Error(t, err)
}

func TestWithoutZstdFramesCarryNoPrefix(t *testing.T) {
	codec := newTestCodec(t)

	encoded, err := codec.EncodeHeartbeatAck()
	require.NoError(t, err)
	var decoded HeartbeatAck
	require.NoError(t, msgpack.Unmarshal(encoded, &decoded))
	assert.Equal(t, HeartbeatAck{T: TypeHeartbeatAck}, decoded)
}

func TestWithZstdReceiverAcceptsBothPrefixes(t *testing.T) {
	codec := newTestCodec(t, CapabilityMsgpack, CapabilityZstd)
	body := frame(t, map[string]any{"t": "heartbeat"})

	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = encoder.Close() })

	plain := append([]byte{framePlain}, body...)
	compressed := append([]byte{frameCompressed}, encoder.EncodeAll(body, nil)...)

	for name, sent := range map[string][]byte{"plain": plain, "compressed": compressed} {
		t.Run(name, func(t *testing.T) {
			got, err := codec.Decode(sent)
			require.NoError(t, err)
			assert.Equal(t, Heartbeat{T: TypeHeartbeat}, got)
		})
	}
}

func TestWithZstdRejectsBadFrames(t *testing.T) {
	codec := newTestCodec(t, CapabilityZstd)

	_, err := codec.Decode(nil)
	assert.Error(t, err, "an empty frame has no prefix to read")

	_, err = codec.Decode(append([]byte{0x02}, frame(t, map[string]any{"t": "heartbeat"})...))
	assert.Error(t, err, "only the two defined prefix values are legal")

	_, err = codec.Decode([]byte{frameCompressed, 0xff, 0xff, 0xff})
	assert.Error(t, err, "a compressed frame that does not inflate is undecodable")
}

// Relay never compresses what it sends, but a negotiated session still needs the prefix on
// every frame, so its own frames say plain.
func TestWithZstdOutboundFramesArePrefixedAndUncompressed(t *testing.T) {
	codec := newTestCodec(t, CapabilityZstd)

	encoded, err := codec.EncodeScopeAdd("sdk-viewB")
	require.NoError(t, err)
	require.NotEmpty(t, encoded)
	assert.Equal(t, framePlain, encoded[0])

	var decoded ScopeAdd
	require.NoError(t, msgpack.Unmarshal(encoded[1:], &decoded))
	assert.Equal(t, ScopeAdd{T: TypeScopeAdd, Key: "sdk-viewB"}, decoded)
}

func TestOutboundEncodersSetTheMessageType(t *testing.T) {
	codec := newTestCodec(t)

	scopeAdd, err := codec.EncodeScopeAdd("sdk-viewB")
	require.NoError(t, err)
	assert.Equal(t, TypeScopeAdd, decodeType(t, scopeAdd))

	scopeRemove, err := codec.EncodeScopeRemove("sdk-viewA")
	require.NoError(t, err)
	assert.Equal(t, TypeScopeRemove, decodeType(t, scopeRemove))

	ack, err := codec.EncodeHeartbeatAck()
	require.NoError(t, err)
	assert.Equal(t, TypeHeartbeatAck, decodeType(t, ack))

	// EncodeError fills the type in even though the caller supplies the rest.
	report, err := codec.EncodeError(ErrorMessage{
		Scope: "sdk-viewA", Code: ErrorInternal, Message: "refs still pending at the cursor",
	})
	require.NoError(t, err)
	assert.Equal(t, TypeError, decodeType(t, report))

	var decoded ErrorMessage
	require.NoError(t, msgpack.Unmarshal(report, &decoded))
	assert.Equal(t, ErrorMessage{
		T: TypeError, Scope: "sdk-viewA", Code: ErrorInternal,
		Message: "refs still pending at the cursor",
	}, decoded)
}

func decodeType(t *testing.T, encoded []byte) string {
	t.Helper()
	var envelope Unknown
	require.NoError(t, msgpack.Unmarshal(encoded, &envelope))
	return envelope.T
}

func TestHasCapability(t *testing.T) {
	negotiated := []string{CapabilityMsgpack, CapabilityBatchRefs}
	assert.True(t, HasCapability(negotiated, CapabilityBatchRefs))
	assert.False(t, HasCapability(negotiated, CapabilityZstd))
	assert.False(t, HasCapability(nil, CapabilityMsgpack))
}

// These three goodbye codes need an operator to edit the configuration, so the session parks
// on them instead of retrying on the ordinary schedule. Every other code, registered or not,
// gets the ordinary rules.
func TestIsConfigurationError(t *testing.T) {
	for _, code := range []string{
		GoodbyeInvalidRoot, GoodbyeRootNotEnvironmentWide, GoodbyeMixedEnvironment,
	} {
		assert.True(t, IsConfigurationError(code), code)
	}
	for _, code := range []string{
		GoodbyeDraining, GoodbyeTooSlow, GoodbyeUnresponsive, GoodbyeHandshakeTimeout,
		GoodbyeRootRevoked, GoodbyePayloadReset, GoodbyeEarlyReconnect,
		GoodbyeProtocolViolation, GoodbyeTransient, GoodbyeInternal, "invented-later", "",
	} {
		assert.False(t, IsConfigurationError(code), code)
	}
}

func boolPtr(v bool) *bool { return &v }

// The schema marks the unfiltered declaration required, and an absent one must be
// distinguishable from a false one. Inferring "filtered" from a missing field would reduce an
// environment-wide scope to serving nothing, which is the exact inference the protocol
// forbids.
func TestServerIntentDistinguishesAnAbsentUnfilteredDeclaration(t *testing.T) {
	codec := newTestCodec(t)

	for name, tc := range map[string]struct {
		sent     map[string]any
		want     bool
		declared bool
	}{
		"declared true": {
			sent: map[string]any{
				"t": "server-intent", "scope": "sdk-env",
				"intentCode": "xfer-full", "unfiltered": true,
			},
			want: true, declared: true,
		},
		"declared false": {
			sent: map[string]any{
				"t": "server-intent", "scope": "sdk-viewA",
				"intentCode": "xfer-full", "unfiltered": false,
			},
			want: false, declared: true,
		},
		"omitted": {
			sent: map[string]any{
				"t": "server-intent", "scope": "sdk-env", "intentCode": "xfer-changes",
			},
			declared: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := codec.Decode(frame(t, tc.sent))
			require.NoError(t, err)
			unfiltered, declared := decoded.(ServerIntent).IsUnfiltered()
			assert.Equal(t, tc.declared, declared)
			assert.Equal(t, tc.want, unfiltered)
		})
	}
}
