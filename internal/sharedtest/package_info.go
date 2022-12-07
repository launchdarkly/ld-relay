// Package sharedtest provides helper code and test data that may be used by tests in all Relay
// components and distributions.
//
// Non-test code should never import this package or any of its subpackages.
//
// To avoid circular references, code in this package cannot reference the relayenv or streams
// package. Any helpers that need to do so must be in a subpackage.
package sharedtest
