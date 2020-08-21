# Contributing to the LaunchDarkly Relay Proxy
 
The LaunchDarkly Relay Proxy's functionality is closely related to the LaunchDarkly SDKs'. We suggest that you review the LaunchDarkly [SDK contributor's guide](https://docs.launchdarkly.com/docs/sdk-contributors-guide) if you are working on this code.

The Relay Proxy code is distributed across several repositories:

* [`github.com/launchdarkly/ld-relay-core`](https://github.com/launchdarkly/ld-relay-core): This contains components that are also shared with Relay Proxy Enterprise. Most of the Relay Proxy functionality is implemented here.
* [`github.com/launchdarkly/ld-relay-config`](https://github.com/launchdarkly/ld-relay-config): This contains configuration types which are also shared with Relay Proxy Enterprise. This is separate from `ld-relay-core` because it is a public, supported API.
* This repository, `ld-relay`, which contains only the high-level interface to the Relay Proxy when it is either built as an application or imported as a library.

## Submitting bug reports and feature requests

The LaunchDarkly SDK team monitors the [issue tracker](https://github.com/launchdarkly/ld-relay/issues) in this repository. File bug reports and feature requests specific to the Relay Proxy in this issue tracker. The SDK team will respond to all newly filed issues within two business days.
 
## Submitting pull requests
 
We encourage pull requests and other contributions from the community. Before you submit a pull request, verify that you've removed that all temporary or unintended code. Don't worry about adding reviewers to the pull request; the LaunchDarkly SDK team will add themselves. The SDK team will acknowledge all pull requests within two business days.
 
## Build instructions
 
### Prerequisites
 
The Relay Proxy should be built against Go 1.13 or newer.

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
 
To build the Relay Proxy and run all unit tests:
```
make test
```

To analyze test coverage:
```
make test-with-coverage
```

To run integration tests, such as a test of Docker deployment:
```
make integration-test
```

### Building with unreleased dependencies

LaunchDarkly uses private mirror repositories for this and other related projects. If it is necessary to temporarily redirect any dependency, such as `github.com/launchdarkly/ld-relay-config`, to the corresponding private repository during development, make sure that this environment variable is set for all local development so that builds can access non-public code:

```
GOPRIVATE="github.com/launchdarkly/*-private"
```
