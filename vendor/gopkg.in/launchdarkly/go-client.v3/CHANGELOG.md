# Change log

All notable changes to the LaunchDarkly Go SDK will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).


## [2.3.0] - 2018-01-31

### Changed
- When evaluating a feature flag, if the client has not yet fully initialized but you are using a Redis store that has already been populated, the client will now use the last known feature data from Redis rather than returning a default value.
- In polling mode, the minimum polling interval is now 30 seconds. Smaller configured values will be adjusted up to the minimum.
- The streaming client will no longer reconnect after detecting an invalidated SDK key.
- Added a build tag, `launchdarkly_no_redis`, which allows building without the Redis dependency.

### Fixed
- Fixed a bug where a previously deleted feature flag might be considered still available.


## [2.2.3] - 2017-12-21

### Added

- Allow user to stop user attributes from being sent in analytics events back to LaunchDarkly.  Set `PrivateAttributeNames` to a list of attributes to avoid sending, or set `AllAttributesPrivate` to `true` to send no attributes.

### Changed

- Accept an interface for the `Logger` configuration option (thanks @ZiaoGeorgeJiang).

## [2.1.0] - 2017-11-16

### Added
- Stop processing streaming events and errors after `Close()`.


## [2.0.0] - 2016-08-08
### Added
- Support for multivariate feature flags. New methods `StringVariation`, `JsonVariation` and `IntVariation` and `Float64Variation` for multivariates.
- New `AllFlags` method returns all flag values for a specified user.
- New `SecureModeHash` function computes a hash suitable for the new LaunchDarkly JavaScript client's secure mode feature.

### Changed
- The `Feature` data model has been replaced with `FeatureFlag`. 

### Deprecated
- The `Toggle` call has been deprecated in favor of `BoolVariation`.
