package wire

import (
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// Compression prefix values. When the session negotiated zstd, every binary frame starts
// with one of these. A receiver accepts both on every frame, because the sender chooses per
// frame whether to compress.
const (
	framePlain      byte = 0x00
	frameCompressed byte = 0x01
)

// maxDecompressedFrame bounds what one compressed frame may inflate to. A frame carries a
// single message, so this is generous for any object LaunchDarkly serves. The bound exists
// because the decompressed length is attacker-controlled in principle, and an unbounded
// allocation driven by a length field is worth refusing at the boundary.
const maxDecompressedFrame = 64 << 20

// Codec encodes and decodes the binary frames that follow the handshake. One Codec belongs
// to one session, because whether frames carry a compression prefix is negotiated per
// session. Close releases the compression resources.
type Codec struct {
	zstd    bool
	decoder *zstd.Decoder
	encoder *zstd.Encoder
}

// NewCodec builds a Codec for a session's negotiated capability set.
func NewCodec(negotiated []string) (*Codec, error) {
	codec := &Codec{zstd: HasCapability(negotiated, CapabilityZstd)}
	if !codec.zstd {
		return codec, nil
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecompressedFrame))
	if err != nil {
		return nil, fmt.Errorf("megastream: cannot create zstd decoder: %w", err)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		decoder.Close()
		return nil, fmt.Errorf("megastream: cannot create zstd encoder: %w", err)
	}
	codec.decoder = decoder
	codec.encoder = encoder
	return codec, nil
}

// Close releases the compression resources. It is safe to call on a Codec that negotiated
// no compression.
func (c *Codec) Close() {
	if c.decoder != nil {
		c.decoder.Close()
	}
	if c.encoder != nil {
		_ = c.encoder.Close()
	}
}

// Decode decodes one binary frame.
//
// The returned value is one of the message types in this package. A message whose type this
// relay does not recognize comes back as Unknown rather than an error, because the protocol
// requires ignoring one instead of failing the connection. Messages the relay itself sends
// are also reported as Unknown: a server has no reason to send them, and treating them as
// unrecognized keeps the connection alive.
func (c *Codec) Decode(frame []byte) (any, error) {
	payload, err := c.payload(frame)
	if err != nil {
		return nil, err
	}
	var envelope Unknown
	if err := msgpack.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("megastream: cannot decode envelope: %w", err)
	}
	switch envelope.T {
	case TypeServerIntent:
		return decodeAs[ServerIntent](payload)
	case TypePutObject:
		return decodeAs[PutObject](payload)
	case TypeDeleteObject:
		return decodeAs[DeleteObject](payload)
	case TypePayloadTransferred:
		return decodeAs[PayloadTransferred](payload)
	case TypeRef:
		return decodeAs[Ref](payload)
	case TypeRefDelete:
		return decodeAs[RefDelete](payload)
	case TypeRefBatch:
		return decodeAs[RefBatch](payload)
	case TypeError:
		return decodeAs[ErrorMessage](payload)
	case TypeHeartbeat:
		return decodeAs[Heartbeat](payload)
	case TypeGoodbye:
		return decodeAs[Goodbye](payload)
	default:
		return envelope, nil
	}
}

// payload strips the compression prefix and inflates the frame when needed.
func (c *Codec) payload(frame []byte) ([]byte, error) {
	if !c.zstd {
		return frame, nil
	}
	if len(frame) == 0 {
		return nil, errors.New("megastream: empty binary frame")
	}
	switch frame[0] {
	case framePlain:
		return frame[1:], nil
	case frameCompressed:
		payload, err := c.decoder.DecodeAll(frame[1:], nil)
		if err != nil {
			return nil, fmt.Errorf("megastream: cannot decompress frame: %w", err)
		}
		return payload, nil
	default:
		return nil, fmt.Errorf("megastream: unknown compression prefix %#x", frame[0])
	}
}

// EncodeScopeAdd builds a frame registering one more credential.
func (c *Codec) EncodeScopeAdd(key string) ([]byte, error) {
	return c.encode(ScopeAdd{T: TypeScopeAdd, Key: key})
}

// EncodeScopeRemove builds a frame deregistering a credential.
func (c *Codec) EncodeScopeRemove(scope string) ([]byte, error) {
	return c.encode(ScopeRemove{T: TypeScopeRemove, Scope: scope})
}

// EncodeHeartbeatAck builds a frame answering a heartbeat.
func (c *Codec) EncodeHeartbeatAck() ([]byte, error) {
	return c.encode(HeartbeatAck{T: TypeHeartbeatAck})
}

// EncodeError builds a frame reporting an error upstream. The caller supplies the code,
// message and, for a scope-level report, the scope. The message must not contain credential
// values, because it is logged and forwarded more widely than a scope tag.
func (c *Codec) EncodeError(report ErrorMessage) ([]byte, error) {
	report.T = TypeError
	return c.encode(report)
}

// encode marshals a message and applies the session's framing.
//
// The relay never compresses what it sends. Its outbound messages are a handful of short
// registrations and acknowledgments, so compressing them would cost more than it saves. When
// the session negotiated zstd the frame still needs its prefix, and the prefix says plain.
func (c *Codec) encode(msg any) ([]byte, error) {
	body, err := msgpack.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("megastream: cannot encode %T: %w", msg, err)
	}
	if !c.zstd {
		return body, nil
	}
	return append([]byte{framePlain}, body...), nil
}

func decodeAs[T any](payload []byte) (any, error) {
	var msg T
	if err := msgpack.Unmarshal(payload, &msg); err != nil {
		return nil, fmt.Errorf("megastream: cannot decode %T: %w", msg, err)
	}
	return msg, nil
}
