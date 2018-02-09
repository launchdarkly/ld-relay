# Change log

All notable changes to the LaunchDarkly Go SDK will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).

## [2.3.0] - 2018-02-09
## Added
- Support for proxied flag evaluation for mobile, client-side, and [unofficial](https://docs.launchdarkly.com/docs/sdk-contributors-guide) SDKs. See the [README](https://github.com/launchdarkly/ld-relay#mobile-and-client-side-flag-evaluation) for configuration instructions.

## [2.2.0] - 2017-06-05
## Changed
- The relay can now act as an [event forwarder](https://github.com/launchdarkly/ld-relay#event-forwarding), which allows PHP clients to synchronously push events to a local relay instead of calling fork+curl to `events.launchdarkly.com`

## [2.1.0] - 2017-11-07
## Changed
- Disabled gzip compression

## [2.0.0] - 2016-08-08
### Added
- Support for multivariate feature flags. New methods `StringVariation`, `JsonVariation` and `IntVariation` and `Float64Variation` for multivariates.
- New `AllFlags` method returns all flag values for a specified user.
- New `SecureModeHash` function computes a hash suitable for the new LaunchDarkly JavaScript client's secure mode feature.

### Changed
- The `Feature` data model has been replaced with `FeatureFlag`.

### Deprecated
- The `Toggle` call has been deprecated in favor of `BoolVariation`.