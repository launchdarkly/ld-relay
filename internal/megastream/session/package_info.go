// Package session owns one MegaStream connection to LaunchDarkly.
//
// A Session dials the socket, performs the handshake, answers heartbeats, honors the
// server's close advice, and reconnects on its own schedule. It exposes the connection as an
// ordered stream of decoded messages plus the two lifecycle events that bracket it.
//
// The session holds no credential state. It asks a caller-supplied provider for the root
// credential and the scopes to register immediately before every handshake, so a reconnect
// presents whatever the caller currently wants served, with the latest recorded selectors.
// Everything credential-shaped, and everything about the data those messages carry, belongs
// to layers above this one.
package session
