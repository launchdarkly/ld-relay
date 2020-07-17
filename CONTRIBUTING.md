# Contributing to the LaunchDarkly Relay Proxy
 
Since the Relay Proxy's functionality is closely related to the SDKs, it may be helpful to review the LaunchDarkly [SDK contributor's guide](https://docs.launchdarkly.com/docs/sdk-contributors-guide) if you are working on this code.
 
## Submitting bug reports and feature requests

The LaunchDarkly SDK team monitors the [issue tracker](https://github.com/launchdarkly/ld-relay/issues) in this repository. Bug reports and feature requests specific to the Relay Proxy should be filed in this issue tracker. The SDK team will respond to all newly filed issues within two business days.
 
## Submitting pull requests
 
We encourage pull requests and other contributions from the community. Before submitting pull requests, ensure that all temporary or unintended code is removed. Don't worry about adding reviewers to the pull request; the LaunchDarkly SDK team will add themselves. The SDK team will acknowledge all pull requests within two business days.
 
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
