// Package registry owns everything credential-shaped on a MegaStream connection.
//
// The session is a transport: it carries an ordered stream of decoded messages and knows
// nothing about credentials. The store holds objects and membership and knows nothing about
// how a credential came to be registered. The registry is in between. It decides what to put
// in a handshake, tracks what the server has said about each credential, drives the store's
// membership lifecycle, and reports which credentials are servable.
//
// It is a reconciler rather than a set of edge-triggered handlers. A desired set arrives
// wholesale from configuration or auto-configuration; wire state arrives as messages; the
// registry repeatedly compares the two and issues whatever the difference calls for. Edge
// triggers do not self-heal -- a registration that failed once stays failed until something
// thinks to retry it -- and the relay has been bitten by exactly that before.
package registry
