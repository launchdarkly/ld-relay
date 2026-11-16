// Package wire implements the MegaStream v1 message types and their framing.
//
// The protocol uses two framings on one WebSocket. The handshake travels as JSON in text
// frames, so that either side can read a rejection from the other regardless of what binary
// format the session negotiates. Every frame after the handshake is a binary frame carrying
// one MessagePack envelope, optionally prefixed with a compression byte.
//
// This package knows the messages and the framing. It holds no session state and performs no
// I/O; the session package owns the socket.
package wire
