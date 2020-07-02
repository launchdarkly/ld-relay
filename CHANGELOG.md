# Change log

All notable changes to the LaunchDarkly Relay will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).

## [5.11.0] - 2020-03-17
### Added:
- Relay now sets the header `X-Accel-Buffering: no` on all streaming responses. If you are using Nginx as a proxy in front of Relay, this tells Nginx to pass streaming data through without buffering; if you are not using Nginx, it has no effect. ([#90](https://github.com/launchdarkly/ld-relay/issues/90))

### Fixed:
- When using a persistent data store such as Redis, if the database was unavailable when Relay initially started and made its first stream connection, a bug caused Relay to give up on retrying and remain in a failed state. This has been fixed so that it will retry the stream connection once it detects that the database is available again (or, if using infinite caching mode, it will leave the same stream connection open and write the already-cached data to the database).
- When Prometheus metrics were enabled, Relay was exposing `/debug/pprof/` endpoints from Go&#39;s `net/http/pprof` on the port used by the Prometheus exporter (prior to v5.6.0, these were also exposed on the main port). These were not part of the Prometheus integration and have been removed, and Relay no longer uses `net/http/pprof`.
- CI builds now verify that Relay builds correctly in all supported Go versions (1.8 through 1.14).

## [5.10.0] - 2020-01-22
### Added:
- When forwarding events, Relay is now able to forward diagnostic data that is sent by newer versions of the SDKs. This has no effect on its behavior with older SDKs.

### Fixed:
- Updated to Go SDK 4.14.2 to fix a bug that could cause spurious "feature store query returned unexpected type" errors to be logged.

## [5.9.4] - 2020-01-15
### Fixed:
- For connections from JavaScript browser SDK versions 2.16.1 and above, Relay now supports CORS requests that include new request headers sent by those SDK versions.
- When forwarding events, Relay now specifies a uniquely identifiable request header when sending events to LaunchDarkly to ensure that events are only processed once, even if Relay sends them two times due to a failed initial attempt.

## [5.9.3] - 2020-01-14
### Fixed:
- When running Relay as a systemd service, the `ld-relay.service` file incorrectly specified the process start-up type as `forking`. Relay does not fork; the correct type is `simple`.


## [5.9.2] - 2020-01-09
### Fixed:
- Relay's logging format was extremely inconsistent: depending on whether a message was related to a specific environment or not, the fields would be in different order and the timestamp was not always at the beginning of the line. This has been normalized so the timestamp (with microseconds) is always first, followed by a tag that is either `[env: name-of-environment]` or `[main]`, then a level such as `INFO:`, and then the message. ([#85](https://github.com/launchdarkly/ld-relay/issues/85))
- The proxy URL parameter was not working in the case of a regular HTTP/HTTPS proxy, as opposed to an NTLM proxy. The standard Go environment variable `HTTPS_PROXY` did work. Both are now usable.

## [5.9.1] - 2020-01-06
### Fixed:
- When forwarding events from the PHP SDK, Relay was not preserving additional information related to experimentation features (supported in PHP SDK 3.6.0 and above). As a result, some flag rules might be included in experimentation data in the LaunchDarkly UI when those rules were not selected to be included. Events from other SDKs were not affected.

## [5.9.0] - 2019-12-20
### Added:
- Added ability to specify DynamoDB endpoint URL for a local instance (`url` property in `dynamodb` config section, or `DYNAMODB_URL` environment variable). ([#82](https://github.com/launchdarkly/ld-relay/issues/82))

### Fixed:
- Fixed a regression that broke the Prometheus exporter in the 5.8.2 release.

## [5.8.2] - 2019-11-07
### Changed:
- Updated to a newer version of the OpenCensus Prometheus exporter. (Thanks, [mightyguava](https://github.com/launchdarkly/ld-relay/pull/70!))
### Fixed:
- When using a persistent feature store (Redis, etc.), if multiple clients request flags at the same time when the flag data is not in the cache, Relay will coalesce these requests so only a single database query is done.

## [5.8.1] - 2019-11-05
### Fixed:
- The `README` incorrectly referred to an environment variable as `DYNAMODB_ENABLED` when it is really `USE_DYNAMODB`. (Thanks, [estraph](https://github.com/launchdarkly/ld-relay/pull/79)!)
- Updated the Alpine base image in the Docker build because some packages in the old image had vulnerabilities. This was previously thought to have been completed in Relay 5.7.0, however, the initial attempt at this version bump was incomplete.

## [5.8.0] - 2019-10-11
### Added:
- It is now possible to specify an infinite cache TTL for persistent feature stores by setting the cache TTL to a negative number, in which case the persistent store will never be read unless Relay restarts. See "Persistent storage" in `README.md`.

### Changed:
- When using a persistent store with an infinite cache TTL (see above), if Relay receives a feature flag update from LaunchDarkly and is unable to write it to the persistent store because of a database outage, it will still update the data in the in-memory cache so it will be available to the application. This is different from the existing behavior when there is a finite cache TTL: in that case, if the database update fails, the in-memory cache will _not_ be updated because the update would be lost as soon as the cache expires.
- When using a persistent store, if there is a database error (indicating that the database may be unavailable, or at least that the most recent update did not get persisted), Relay will continue to monitor the database availability. Once it returns to normal, if the cache TTL is finite, Relay will restart the LaunchDarkly connection to ensure that it receives and persists a full set of flag data; if the cache TTL is infinite, it will assume the cache is up to date and will simply write it to the database. See "Persistent storage" in `README.md`.


## [5.7.0] - 2019-09-18
### Added:
- The `exitAlways` configuration property (or `EXIT_ALWAYS`) variable causes the Relay Proxy to quit as soon as it has initialized all environments. This can be used for testing (just to make sure everything is working), or to perform a single poll of flags and put them into Redis or another database.
- The endpoints used by the PHP SDK (when it is not in LDD mode) were not previously supported. They are now. There is a new `ttlMinutes` property to configure caching behavior for PHP, similar to the TTL property on the LaunchDarkly dashboard. ([#68](https://github.com/launchdarkly/ld-relay/issues/68))
- The `logLevel` configuration properties (or `LOG_LEVEL` and `LD_LOG_LEVEL_EnvName`) allow you to request more or less verbose logging.
- If debug-level logging is enabled, the Relay Proxy will log every incoming HTTP request. ([#51](https://github.com/launchdarkly/ld-relay/issues/51))

### Changed:
- Log messages related to a specific environment are now prefixed with `[env: EnvironmentName]` (where `EnvironmentName` is the name you specified for that environment in your configuration) rather than `[LaunchDarkly Relay (SdkKey ending with xxxxx)]` (where `xxxxx` was the last 5 characters of the SDK key).

### Fixed:
- Updated Alpine base image in Docker build because some packages in the old image had vulnerabilities. (Thanks, [e96wic](https://github.com/launchdarkly/ld-relay/pull/74)!)
- Fixed a CI build problem for Go 1.8.

## [5.6.1] - 2019-08-05
### Fixed:
- Enabling TLS for Redis as a separate option was not working; it would only work if you specified a `rediss://` secure URL. Both methods now work. ([#71](https://github.com/launchdarkly/ld-relay/issues/71))

## [5.6.0] - 2019-08-02
### Added:
- You can now specify a proxy server URL in the configuration file, or in a Docker environment variable.
- Also, Relay now respects the standard Go environment variables `HTTP_PROXY` and `HTTPS_PROXY`.
- You may specify additional CA certificates for outgoing HTTPS connections.
- Relay now supports proxy servers that use NTLM authentication.
- You may specify a Redis password, or turn on TLS for Redis, without modifying the Redis URL.
- See `README.md` for details on configuring all of the above features.
 
### Changed:
- Extracted `Config` structs so that they could be configured programmatically when Relay is used as a library. (Thanks, [mightyguava](https://github.com/launchdarkly/ld-relay/pull/65)!)
 
### Fixed:
- The endpoints used by the client-side JavaScript SDKs were incorrectly returning _all_ flags, rather than only the flags with the "client-side" property (as the regular LaunchDarkly service does). ([#63](https://github.com/launchdarkly/ld-relay/issues/63))

## [5.5.2] - 2019-05-15
### Added
- Added documentation for the `REDIS_URL` environment variable to the README.
### Changed
- Replaced import paths for `gopkg.in/launchdarkly/go-client.v4` with `gopkg.in/launchdarkly/go-server-sdk.v4`. The newer import path reflects the new repository URL for the LaunchDarkly Server-side SDK for Go.

## [5.5.1] - 2018-12-19
### Fixed:
- When proxying events, the Relay now preserves information about what kind of platform they came from: server-side, mobile, or browser. Previously, it delivered all events to LaunchDarkly as if they were server-side events, which could cause your usage statistics to be wrong. ([#53](https://github.com/launchdarkly/ld-relay/issues/53))
- The `docker-entrypoint.sh` script, which is meant to run with any `sh`-compatible shell, contained some `bash` syntax that does not work in every shell.
- If the configuration is invalid because more than one database is selected (e.g., Redis + DynamoDB), the Relay will report an error. Previously, it picked one of the databases and ignored the others.
- Clarified documentation about persistent feature stores, and boolean environment variables.

## [5.5.0] - 2018-11-27
### Added
- The relay now supports additional database integrations: Consul and DynamoDB. As with the existing Redis integration, these can be used to store feature flags that will be read from the same database by a LaunchDarkly SDK client (as described [here](https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store)), or simply as a persistence mechanism for the relay itself. See `README.MD` for configuration details.
- It is now possible to specify a Redis URL in `$REDIS_URL`, rather than just `$REDIS_HOST` and `$REDIS_PORT`, when running the Relay in Docker. (Thanks, [lukasmrtvy](https://github.com/launchdarkly/ld-relay/pull/48)!)

### Fixed
- When deployed in a Docker container, the relay no longer runs as the root user; instead, it creates a user called `ldr-user`. Also, the configuration file is now in `/ldr` instead of in `/etc`. (Thanks, [sdif](https://github.com/launchdarkly/ld-relay/pull/45)!)

## [5.4.4] - 2018-11-19

### Fixed
- Fixed a bug that would cause the relay to hang after the LaunchDarkly client finished initializing, if you had previously queried any relay endpoint _before_ the client finished initializing (e.g. if there was a delay due to network problems and you checked the status resource during that delay).

## [5.4.3] - 2018-09-97

### Added 
- Return flag evaluation reasons for mobile and client-side endpoints when `withReasons=true` query param is set.
- Added support for metric collection configuration in docker entrypoint script. Thanks @matthalbersma for the suggestion.

### Fixes
- Fix internal relay metrics event field.

## [5.4.2] - 2018-08-29

### Changed
- Use latest go client (4.3.0).
- Preserves the "client-side-enabled" property of feature flags so that the new SDK method AllFlagsState() - which has an option for selecting only client-side flags - will work correctly when using the relay.

### Fixes
- Ensure that server-side eval endpoints don't panic (work around gorilla mux bug)


## [5.4.1] - 2018-08-24

### Fixed
- Strip trailing slash that was breaking default urls for polling and streaming.
- If set X-LaunchDarkly-User-Agent will be used instead of the User-Agent header for metrics.
- Documentation improvements.

## [5.4.0] - 2018-08-09

### Added
- The Relay now supports the streaming endpoint used by current mobile SDKs. It does not fully proxy the stream, but will send an event causing mobile SDKs to re-request their flags when flags have changed.

## [5.3.1] - 2018-02-03

### Fixed

- Route metrics are now tagged correctly with the route name.
- Datadog exporter now works with 32-bit builds.


## [5.3.0] - 2018-07-30

### Added

- The Relay now supports exporting metrics and traces to Datadog, Prometheus, and Stackdriver. See the README for configuration instructions.

### Changed

- Packages intended for internal relay use are now marked as internal and no longer accessible exsternally.
- The package is now released using [goreleaser](https://goreleaser.com/).

## [5.2.0] - 2018-07-13

### Added
- The Redis server location-- and logical database-- can now optionally be specified as a URL (`url` property in `[redis]` section) rather than a hostname and port.
- The relay now sends usage metrics back to LaunchDarkly at 1-minute intervals.

### Fixed
- The /sdk/goals/<envId> endpoint now supports caching and repeats any headers it received to the client.

