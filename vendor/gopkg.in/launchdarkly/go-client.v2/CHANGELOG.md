# Change log

All notable changes to the LaunchDarkly Go SDK will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).

## [2.0.0] - 2016-08-08
### Added
- Support for multivariate feature flags. New methods `StringVariation`, `JsonVariation` and `IntVariation` and `Float64Variation` for multivariates.
- New `AllFlags` method returns all flag values for a specified user.
- New `SecureModeHash` function computes a hash suitable for the new LaunchDarkly JavaScript client's secure mode feature.

### Changed
- The `Feature` data model has been replaced with `FeatureFlag`. 

### Deprecated
- The `Toggle` call has been deprecated in favor of `BoolVariation`.