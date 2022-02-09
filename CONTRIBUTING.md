# Contributing to the LaunchDarkly Relay Proxy
 
The LaunchDarkly Relay Proxy's functionality is closely related to the LaunchDarkly SDKs'. We suggest that you review the LaunchDarkly [SDK contributor's guide](https://docs.launchdarkly.com/docs/sdk-contributors-guide) if you are working on this code.
 
## Submitting bug reports and feature requests

The LaunchDarkly SDK team monitors the [issue tracker](https://github.com/launchdarkly/ld-relay/issues) in this repository. File bug reports and feature requests specific to the Relay Proxy in this issue tracker. The SDK team will respond to all newly filed issues within two business days.
 
## Submitting pull requests
 
We encourage pull requests and other contributions from the community. Before you submit a pull request, verify that you've removed that all temporary or unintended code. Don't worry about adding reviewers to the pull request; the LaunchDarkly SDK team will add themselves. The SDK team will acknowledge all pull requests within two business days.
 
## Build instructions
 
### Prerequisites
 
The Relay Proxy should be built against Go 1.16 or newer.

### Building

To build the Relay Proxy without running any tests:
```
make
```

If you wish to clean your working directory between builds, you can clean it by running:
```
make clean
```

To run the linter:
```
make lint
```

### Testing
 
To build the Relay Proxy and run all unit tests that don't require a persistent data store:
```
make test
```

To include tests of persistent data store behavior using Redis, assuming a Redis server is running at `localhost:6379`:
```
TAGS=redis_unit_tests make test
```

To analyze test coverage:
```
make test-with-coverage
```

To run integration tests, such as a test of Docker deployment:
```
make integration-test
```

### Coding guidelines

As this is a larger codebase than most LaunchDarkly open-source projects, we have a few guidelines to maintain internal consistency.

* No packages outside of `internal/` should export any symbols other than the ones that are necessary to support usage of the Relay Proxy [as a library](./docs/in-app.md). Anything else that is visible becomes part of the supported external API for this project and can't be changed without a new major version, so be careful not to export any irrelevant implementation details. Anything within `internal/` can safely be changed.
* Package imports should be grouped as follows: 1. all built-in Go packages; 2. all packages that are part of this repository (`github.com/launchdarkly/ld-relay/...`); 3. all other LaunchDarkly packages (`github.com/launchdarkly/...`, `gopkg.in/launchdarkly/...`); 4. all third-party packages.

### Runtime platform versions (Go and Alpine) for Docker

The published `ld-relay` Docker image embeds specific versions of the Alpine OS and the Go runtime. We update these to take advantage of patch releases for both Alpine and Go.

These versions are specified in several places. For the published `ld-relay` image:

* The Alpine version is specified by the `FROM` line in `Dockerfile.goreleaser`.
* The Go version is specified by the `image` property in `.ldrelease/config.yml`. Basically, we run a Docker container with some version of Go in it, and within that container we will be running `goreleaser`. Then the `goreleaser` tool will look at `Dockerfile.goreleaser` to provide the base image, and it will embed whatever version of the Go runtime it is running on in the published executable.

When we change these versions, we should also update our test builds to match the versions we are releasing with:

* In `.circleci/config.yml`, update the default value of `go-release-version`.
* In `Dockerfile` (which is used for CI tests, not for the release), update the `FROM` line to an image in the format `golang:$(GO_VERSION)-alpine$(ALPINE_VERSION)`.
