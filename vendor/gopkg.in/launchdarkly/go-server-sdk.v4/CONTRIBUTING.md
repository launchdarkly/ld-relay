Contributing to the LaunchDarkly Server-side SDK for Go
================================================
 
LaunchDarkly has published an [SDK contributor's guide](https://docs.launchdarkly.com/docs/sdk-contributors-guide) that provides a detailed explanation of how our SDKs work. See below for additional information on how to contribute to this SDK.
 
Submitting bug reports and feature requests
------------------

The LaunchDarkly SDK team monitors the [issue tracker](https://github.com/launchdarkly/go-server-sdk/issues) in the SDK repository. Bug reports and feature requests specific to this SDK should be filed in this issue tracker. The SDK team will respond to all newly filed issues within two business days.
 
Submitting pull requests
------------------
 
We encourage pull requests and other contributions from the community. Before submitting pull requests, ensure that all temporary or unintended code is removed. Don't worry about adding reviewers to the pull request; the LaunchDarkly SDK team will add themselves. The SDK team will acknowledge all pull requests within two business days.
 
Build instructions
------------------
 
### Prerequisites
 
The SDK should be built against Go 1.8 or newer.

Note that the SDK's public import path is `gopkg.in/launchdarkly/go-server-sdk.v4` (using the [`gopkg.in`](https://labix.org/gopkg.in) service as a simple way to pin to a major version). Since it does not use Go modules, and it references its own import path in imports between packages, this means that in order to build it you must check it out at `$GOPATH/src/gopkg.in/launchdarkly/go-server-sdk.v4`-- not `$GOPATH/src/github.com/launchdarkly/go-server-sdk`.

Dependencies are managed with `dep`; after changing any imports, run `dep ensure`.

### Building

To build the SDK without running any tests:
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

If you add any new dependencies to the SDK, use `dep ensure` to ensure that they are copied to `vendor/`.

### Testing
 
To build the SDK and run all unit tests:
```
make test
```

By default, the full unit test suite includes live tests of the integrations for Consul, DynamoDB, and Redis. Those tests expect you to have instances of all of those databases running locally. To skip them, set the environment variable `LD_SKIP_DATABASE_TESTS=1` before running the tests.
