// Package configsource contains test helpers that mock the source Relay loads its
// environment configuration from: the Relay Auto Config (RAC) SSE stream.
//
// These live in sharedtest/configsource rather than sharedtest itself because they reference the
// envfactory package, which transitively imports relayenv and streams. Putting them in a subpackage
// keeps the top-level sharedtest package importable by relayenv and streams without a circular
// reference (see sharedtest/package_info.go).
//
// Non-test code should never import this package.
package configsource
