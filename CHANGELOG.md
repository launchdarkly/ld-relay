# Change log

All notable changes to the LaunchDarkly Relay will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).

## [5.2.0] - 2018-07-13

### Added
- The Redis server location-- and logical database-- can now optionally be specified as a URL (`url` property in `[redis]` section) rather than a hostname and port.
- The relay now sends usage metrics back to LaunchDarkly at 1-minute intervals.

### Fixed
- The /sdk/goals/<envId> endpoint now supports caching and repeats any headers it received to the client.

