// Package integrationtests defines end-to-end integration tests between a Relay instance and the LD
// services (normally the staging ones). These tests only run if the "integrationtests" build tag is
// set.
//
// These are separate from the other kind of integration test we do in CI, the "docker-smoke-test"
// job which verifies that the binaries we build for release can be run in Docker; that one does not
// involve testing Relay's interactions with LD.
//
// The tests are configured with the following environment variables:
//
// - LD_BASE_URL, LD_STREAM_URL: optional LD service base URLs (default: staging)
//
// - LD_API_TOKEN: required, must be an API access token with admin permission; this should be set in
// CircleCI as part of the project configuration
//
// - RELAY_TAG_OR_SHA: optional branch/tag name or commit hash in the ld-relay-private repo, to test
// that specific version rather than the working copy that the tests are running in
//
// - HTTP_LOGGING: set this to "true" to enable verbose logging of all HTTP requests made by the
// integration tests (to the LaunchDarkly API and to Relay).
package integrationtests
