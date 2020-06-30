# Contributing to this project
 
LaunchDarkly has published an [SDK contributor's guide](https://docs.launchdarkly.com/docs/sdk-contributors-guide) that provides a detailed explanation of how our SDKs work. See below for additional information on how to contribute to this project.
 
## Submitting bug reports and feature requests

The LaunchDarkly SDK team monitors the [issue tracker](https://github.com/launchdarkly/go-sdk-events/issues) in tis repository. Bug reports and feature requests specific to this project should be filed in this issue tracker. The SDK team will respond to all newly filed issues within two business days. For issues or requests that are more generally related to the LaunchDarkly Go SDK, rather than specifically for the code in this repository, please use the [`go-server-sdk`](https://github.com/launchdarkly/go-server-sdk) repository.
 
## Submitting pull requests
 
We encourage pull requests and other contributions from the community. Before submitting pull requests, ensure that all temporary or unintended code is removed. Don't worry about adding reviewers to the pull request; the LaunchDarkly SDK team will add themselves. The SDK team will acknowledge all pull requests within two business days.
 
## Build instructions
 
### Prerequisites
 
This project should be built against Go 1.13 or newer.

Note that the public import path is `gopkg.in/launchdarkly/go-sdk-events.v1` (using the [`gopkg.in`](https://labix.org/gopkg.in) service as a simple way to pin to a major version). Since it does not use Go modules, and it references its own import path in imports between packages, this means that in order to build it you must check it out at `$GOPATH/src/gopkg.in/launchdarkly/go-sdk-events.v1`-- not `$GOPATH/src/github.com/launchdarkly/go-sdk-events`.

### Building

To build the project without running any tests:
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
 
To build and run all unit tests:
```
make test
```
