package wire

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"
)

// The schema types the version fields as a number rather than an integer, so an independent
// implementation may reasonably encode one either way. Both have to decode: this is the same
// class of interop hazard as the object field's bin-versus-str ambiguity.
func TestPayloadVersionAcceptsIntegerAndFloatEncodings(t *testing.T) {
	for name, sent := range map[string]any{
		"small integer": 13,
		"large integer": int64(1 << 40),
		"float":         float64(19),
		"large float":   float64(1 << 40),
		"zero":          0,
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := msgpack.Marshal(sent)
			require.NoError(t, err)

			var got PayloadVersion
			require.NoError(t, msgpack.Unmarshal(encoded, &got))

			var want PayloadVersion
			switch typed := sent.(type) {
			case int:
				want = PayloadVersion(typed)
			case int64:
				want = PayloadVersion(typed)
			case float64:
				want = PayloadVersion(typed)
			}
			assert.Equal(t, want, got)
		})
	}
}

// A version is always a whole number. A fractional or non-finite value means the sender is
// not describing a payload version, so it is worth refusing rather than truncating.
func TestPayloadVersionRejectsValuesThatAreNotWholeNumbers(t *testing.T) {
	for name, sent := range map[string]float64{
		"fractional":        19.5,
		"not a number":      math.NaN(),
		"positive infinity": math.Inf(1),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := msgpack.Marshal(sent)
			require.NoError(t, err)
			var got PayloadVersion
			assert.Error(t, msgpack.Unmarshal(encoded, &got))
		})
	}
}

// The whole message has to decode either way, not just the field in isolation.
func TestVersionCarryingMessagesDecodeWithEitherEncoding(t *testing.T) {
	codec, err := NewCodec(nil)
	require.NoError(t, err)
	t.Cleanup(codec.Close)

	for name, version := range map[string]any{"integer": 19, "float": float64(19)} {
		t.Run(name, func(t *testing.T) {
			put, err := msgpack.Marshal(map[string]any{
				"t": "put-object", "base": "payload-1", "kind": "flag",
				"key": "f1", "version": version, "object": []byte(`{"key":"f1"}`),
			})
			require.NoError(t, err)
			decoded, err := codec.Decode(put)
			require.NoError(t, err)
			assert.Equal(t, PayloadVersion(19), decoded.(PutObject).Version)

			deleted, err := msgpack.Marshal(map[string]any{
				"t": "delete-object", "base": "payload-1",
				"kind": "flag", "key": "f9", "version": version,
			})
			require.NoError(t, err)
			decoded, err = codec.Decode(deleted)
			require.NoError(t, err)
			assert.Equal(t, PayloadVersion(19), decoded.(DeleteObject).Version)

			cursor, err := msgpack.Marshal(map[string]any{
				"t": "payload-transferred", "scope": "sdk-env",
				"state": "sel-1", "version": version,
			})
			require.NoError(t, err)
			decoded, err = codec.Decode(cursor)
			require.NoError(t, err)
			assert.Equal(t, PayloadVersion(19), decoded.(PayloadTransferred).Version)
		})
	}
}
