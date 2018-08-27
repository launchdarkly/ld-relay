# Change log

All notable changes to the LaunchDarkly Go SDK will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).

## [4.3.0] - 2018-08-27
### Added:
- The new `LDClient` method `AllFlagsState()` should be used instead of `AllFlags()` if you are passing flag data to the front end for use with the JavaScript SDK. It preserves some flag metadata that the front end requires in order to send analytics events correctly. Versions 2.5.0 and above of the JavaScript SDK are able to use this metadata, but the output of `AllFlagsState()` will still work with older versions.
- The `AllFlagsState()` method also allows you to select only client-side-enabled flags to pass to the front end, by using the option `ClientSideOnly`.
- The new `LDClient` methods `BoolVariationDetail`, `IntVariationDetail`, `Float64VariationDetail`, `StringVariationDetail`, and `JsonVariationDetail` allow you to evaluate a feature flag (using the same parameters as you would for `BoolVariation`, etc.) and receive more information about how the value was calculated. This information is returned in an `EvaluationDetail` object, which contains both the result value and an `EvaluationReason` which will tell you, for instance, if the user was individually targeted for the flag or was matched by one of the flag's rules, or if the flag returned the default value due to an error.

### Deprecated:
- `LDClient.AllFlags()`, `EvalResult`, `FeatureFlag.Evaluate`, `FeatureFlag.EvaluateExplain`

## [4.2.2] - 2018-08-03
### Fixed:
- Fixed a bug that caused a panic if an I/O error occurred while reading the response body for a polling request.
- Fixed a bug that caused a panic if a prerequisite feature flag evaluated to a non-scalar value (array or map/hash).
- Receiving an HTTP 400 error from LaunchDarkly should not make the client give up on sending any more requests to LaunchDarkly (unlike a 401 or 403).

## [4.2.1] - 2018-06-27
### Fixed:
- Polling processor regressed to polling only once in release 4.1.0.  This has been fixed.



## [4.2.0] - 2018-06-26
### Changed:
- The client now treats most HTTP 4xx errors as unrecoverable: that is, after receiving such an error, it will not make any more HTTP requests for the lifetime of the client instance, in effect taking the client offline. This is because such errors indicate either a configuration problem (invalid SDK key) or a bug, which is not likely to resolve without a restart or an upgrade. This does not apply if the error is 400, 408, 429, or any 5xx error.

## [4.1.0] - 2018-06-14
### Changed

The Go client now depends on the latest release of 1.0.0 of LaunchDarkly fork of eventsource, which supports the Close() method.

### Fixed

- Calling Close on the client now immediately closes the streaming connection, if the client is in streaming mode.
- During initialization, if the client receives a 401 error from LaunchDarkly (indicating an invalid SDK key), the client constructor will return immediately rather than waiting for a timeout, since there is no way for the client to recover if the SDK key is wrong. The Initialized() method will return false in this case.
- More generally, the error response for creating a client will also indicate that initialization has failed if the client has not yet been initialized by the UpdateProcessor.

## [4.0.0] - 2018-05-10

### Changed
- To reduce the network bandwidth used for analytics events, feature request events are now sent as counters rather than individual events, and user details are now sent only at intervals rather than in each event. These behaviors can be modified through the LaunchDarkly UI and with the new configuration option `InlineUsersInEvents`. For more details, see [Analytics Data Stream Reference](https://docs.launchdarkly.com/v2.0/docs/analytics-data-stream-reference).
- When sending analytics events, if there is a connection error or an HTTP 5xx response, the client will try to send the events again one more time after a one-second delay.
- The `Close` method on the client now conforms to the `io.Closer` interface.

### Added
- The new global `VersionedDataKinds` is an array of all existing `VersionedDataKind` instances. This is mainly useful if you are writing a custom `FeatureStore` implementation. (Thanks, [mlafeldt](https://github.com/launchdarkly/go-client/pull/117)!)


## [3.1.0] - 2018-03-19
### Added
- Convenience functions `NewUser` and `NewAnonymousUser`, for creating a user struct given only the key. (Thanks, [mlafeldt](https://github.com/launchdarkly/go-client/pull/109)!)
### Fixed
- In the Redis feature store, fixed a synchronization problem that could cause a feature flag update to be missed if several of them happened in rapid succession.
- Fixed errors in the Readme example code. (Thanks, [mlafeldt](https://github.com/launchdarkly/go-client/pull/110)!)

## [3.0.0] - 2018-02-19

### Added
- Support for a new LaunchDarkly feature: reusable user segments.
- The mechanism by which the client retrieves feature and segment data from the server is now customizable through an interface, `UpdateProcessor`. This will be used in future to support test fixtures.

### Changed
- The `FeatureStore` interface has been changed to support user segment data as well as feature flags. Existing code that uses `InMemoryFeatureStore` or `RedisFeatureStore` should work as before, but custom feature store implementations will need to be updated.
- Logging is now done through an interface, `Logger`, instead of directly referencing `log.Logger`. Existing code that uses `log.Logger` should still work as before.



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
