// Package testsuites contains shared test suites that should be run against every version of Relay to
// validate the behavior of the core code. These are run in two ways:
//
// 1. Within the Core project, by core_run_endpoints_test.go. This validates that the test suites are
// correct with regard to the core components alone.
//
// 2. Within the dependent Relay projects (this is why they are not implemented in _test.go files,
// because it's not possible to access _test code from another project). This validates that each Relay
// version has correctly set up its core components.
package testsuites
