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
 
The SDK builds with [Gradle](https://gradle.org/) and should be built against Go 1.8 or newer.
 
### Building
 
To build the SDK without running any tests:
```
go build
```

If you wish to clean your working directory between builds, you can clean it by running:
```
go clean
```

When building the SDK, you may need to download additional dependencies. You can do so by using `go get`.
```
go get <DEPENDENCY_PACKAGE_NAME_HERE>
```

 
### Testing
 
To build the SDK and run all unit tests:
```
go test
```
