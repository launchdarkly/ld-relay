package wire

import (
	"encoding/json"
	"fmt"
)

// The handshake travels as JSON in text frames so that either side can read the other's
// rejection whatever binary format the session goes on to negotiate.
//
// Hello and Welcome name the message in a "type" field, while every envelope after the
// handshake uses the compact "t". Goodbye and ErrorMessage keep "t" even here, because they
// also travel as envelopes once the session is established and one shape for each message is
// worth more than consistency across the two framings.

// EncodeHello builds the handshake frame. It fills in the message type and protocol version,
// so a caller cannot get either wrong.
func EncodeHello(hello Hello) ([]byte, error) {
	hello.Type = TypeHello
	hello.Version = ProtocolVersion
	frame, err := json.Marshal(hello)
	if err != nil {
		return nil, fmt.Errorf("megastream: cannot encode hello: %w", err)
	}
	return frame, nil
}

// DecodeHandshake decodes a text frame the server sends before the session is established.
// It returns a Welcome, a Goodbye or an ErrorMessage.
func DecodeHandshake(frame []byte) (any, error) {
	var envelope struct {
		Type string `json:"type"`
		T    string `json:"t"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil {
		return nil, fmt.Errorf("megastream: cannot decode handshake frame: %w", err)
	}
	name := envelope.Type
	if name == "" {
		name = envelope.T
	}
	switch name {
	case TypeWelcome:
		return unmarshalHandshake[Welcome](frame)
	case TypeGoodbye:
		return unmarshalHandshake[Goodbye](frame)
	case TypeError:
		return unmarshalHandshake[ErrorMessage](frame)
	case "":
		return nil, fmt.Errorf("megastream: handshake frame names no message type")
	default:
		return nil, fmt.Errorf("megastream: unexpected handshake message %q", name)
	}
}

func unmarshalHandshake[T any](frame []byte) (any, error) {
	var msg T
	if err := json.Unmarshal(frame, &msg); err != nil {
		return nil, fmt.Errorf("megastream: cannot decode %T: %w", msg, err)
	}
	return msg, nil
}
