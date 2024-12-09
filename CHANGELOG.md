# Change log

All notable changes to the LaunchDarkly Relay will be documented in this file. This project adheres to [Semantic Versioning](http://semver.org).

## [8.10.4](https://github.com/launchdarkly/ld-relay/compare/v8.10.3...v8.10.4) (2024-12-09)


### Bug Fixes

* **deps:** update Dockerfiles from 3.20.3 to alpine:3.21.0 ([#465](https://github.com/launchdarkly/ld-relay/issues/465)) ([b8237c4](https://github.com/launchdarkly/ld-relay/commit/b8237c4e769a3e2ee344cb968a1188d329f91fd4))

## [8.10.3](https://github.com/launchdarkly/ld-relay/compare/v8.10.2...v8.10.3) (2024-12-04)


### Bug Fixes

* **deps:** bump supported Go versions to 1.23.4 and 1.22.10 ([#463](https://github.com/launchdarkly/ld-relay/issues/463)) ([0b22f62](https://github.com/launchdarkly/ld-relay/commit/0b22f628af9ad8dc19fd80bec47232ddf8066277))

## [8.10.2](https://github.com/launchdarkly/ld-relay/compare/v8.10.1...v8.10.2) (2024-11-07)


### Bug Fixes

* **deps:** bump supported Go versions to 1.23.3 and 1.22.9 ([#461](https://github.com/launchdarkly/ld-relay/issues/461)) ([3318a21](https://github.com/launchdarkly/ld-relay/commit/3318a215c8ae96104dd8c22babd33c86e10f5e7a))

## [8.10.1](https://github.com/launchdarkly/ld-relay/compare/v8.10.0...v8.10.1) (2024-10-23)


### Bug Fixes

* **build:** add windows binaries ([388feb4](https://github.com/launchdarkly/ld-relay/commit/388feb45a1e211e6794f7ad0f239362608d88fa5))

## [8.10.0](https://github.com/launchdarkly/ld-relay/compare/v8.9.6...v8.10.0) (2024-10-17)


### Features

* add support for client-side prerequisite events ([#452](https://github.com/launchdarkly/ld-relay/issues/452)) ([9dea4b5](https://github.com/launchdarkly/ld-relay/commit/9dea4b584793cf24784e5859a94e8cee4e634f1f))

## [8.9.6](https://github.com/launchdarkly/ld-relay/compare/v8.9.5...v8.9.6) (2024-10-02)


### Bug Fixes

* **deps:** bump supported Go versions to 1.23.2 and 1.22.8 ([#447](https://github.com/launchdarkly/ld-relay/issues/447)) ([59203f5](https://github.com/launchdarkly/ld-relay/commit/59203f56407a0c4d5905f76e803c43ce27f8f310))

## [8.9.5](https://github.com/launchdarkly/ld-relay/compare/v8.9.4...v8.9.5) (2024-09-17)


### Bug Fixes

* **deps:** bump supported Go versions to 1.23.1 and 1.22.7 ([#441](https://github.com/launchdarkly/ld-relay/issues/441)) ([1ab08a2](https://github.com/launchdarkly/ld-relay/commit/1ab08a267d9cc85c76031a60ef457c7723110014))
* **deps:** update Dockerfiles from alpine:3.20.2 to alpine:3.20.3 ([#444](https://github.com/launchdarkly/ld-relay/issues/444)) ([613ee33](https://github.com/launchdarkly/ld-relay/commit/613ee33f3ca3f339346f169d833e069bac7e02c6))

## [8.9.4](https://github.com/launchdarkly/ld-relay/compare/v8.9.3...v8.9.4) (2024-09-06)


### Bug Fixes

* Include expires header on 304 cache hit ([#438](https://github.com/launchdarkly/ld-relay/issues/438)) ([b24370e](https://github.com/launchdarkly/ld-relay/commit/b24370e951df90b35fca1efff49d940843d01d6e))

## [8.9.3](https://github.com/launchdarkly/ld-relay/compare/v8.9.2...v8.9.3) (2024-08-14)


### Bug Fixes

* **deps:** bump supported Go versions to 1.23.0 and 1.22.6 ([#432](https://github.com/launchdarkly/ld-relay/issues/432)) ([e246aec](https://github.com/launchdarkly/ld-relay/commit/e246aec7e2b1e1c24a872875331ea929e17996ae))

## [8.9.2](https://github.com/launchdarkly/ld-relay/compare/v8.9.1...v8.9.2) (2024-08-07)


### Bug Fixes

* **deps:** bump supported Go versions to 1.22.6 and 1.21.13 ([#426](https://github.com/launchdarkly/ld-relay/issues/426)) ([c3097d4](https://github.com/launchdarkly/ld-relay/commit/c3097d48ad1f40fb8ae39d0edf5a7e52602585dc))

## [8.9.1](https://github.com/launchdarkly/ld-relay/compare/v8.9.0...v8.9.1) (2024-08-05)


### Bug Fixes

* **deps:** update Dockerfiles from alpine:3.20.1 to alpine:3.20.2 ([#419](https://github.com/launchdarkly/ld-relay/issues/419)) ([3d6e5ae](https://github.com/launchdarkly/ld-relay/commit/3d6e5aea0d318fdca11f4ade275a17f85ea84a22))
* don't panic if envContext is closed twice ([#417](https://github.com/launchdarkly/ld-relay/issues/417)) ([36f3168](https://github.com/launchdarkly/ld-relay/commit/36f3168b3d3a6ab86535faeae774adce741e057d))

## [8.9.0](https://github.com/launchdarkly/ld-relay/compare/v8.8.2...v8.9.0) (2024-07-25)


### Features

* Add new `MaxInboundPayloadSize` configuration to limit event payload sizes ([#364](https://github.com/launchdarkly/ld-relay/issues/364)) ([4803e76](https://github.com/launchdarkly/ld-relay/commit/4803e76abc8c26c32ed585a3a7a8684ff01a1495))
* gzip event payloads before sending to upstream LaunchDarkly APIs ([#364](https://github.com/launchdarkly/ld-relay/issues/364)) ([4803e76](https://github.com/launchdarkly/ld-relay/commit/4803e76abc8c26c32ed585a3a7a8684ff01a1495))
* Support receiving compressed event payloads ([#364](https://github.com/launchdarkly/ld-relay/issues/364)) ([4803e76](https://github.com/launchdarkly/ld-relay/commit/4803e76abc8c26c32ed585a3a7a8684ff01a1495))


### Bug Fixes

* Bump minimum go version to 1.21 ([#364](https://github.com/launchdarkly/ld-relay/issues/364)) ([4803e76](https://github.com/launchdarkly/ld-relay/commit/4803e76abc8c26c32ed585a3a7a8684ff01a1495))

## [8.8.2](https://github.com/launchdarkly/ld-relay/compare/v8.8.1...v8.8.2) (2024-07-15)


### Bug Fixes

* redact password in logs if specified as part of URL ([#413](https://github.com/launchdarkly/ld-relay/issues/413)) ([0471d51](https://github.com/launchdarkly/ld-relay/commit/0471d51a315637df91d38f42afedd0e170cdefad))

## [8.8.1](https://github.com/launchdarkly/ld-relay/compare/v8.8.0...v8.8.1) (2024-07-10)


### Bug Fixes

* **deps:** bump supported Go versions to 1.22.5 and 1.21.12 ([#411](https://github.com/launchdarkly/ld-relay/issues/411)) ([02c0a7e](https://github.com/launchdarkly/ld-relay/commit/02c0a7eb93ece5c288aaa754f9ec08785d0631e6))

## [8.8.0](https://github.com/launchdarkly/ld-relay/compare/v8.7.1...v8.8.0) (2024-06-25)


### Features

* offline mode key rotation ([#408](https://github.com/launchdarkly/ld-relay/issues/408)) ([b3f03a4](https://github.com/launchdarkly/ld-relay/commit/b3f03a44bb071ba263e52eafadebdf121643364b))
* periodic key revocation ([#401](https://github.com/launchdarkly/ld-relay/issues/401)) ([92033e9](https://github.com/launchdarkly/ld-relay/commit/92033e90ab480c3920c69fa122bdf1499a3a8e05))


### Bug Fixes

* offline mode would spam logs when file changes ([#406](https://github.com/launchdarkly/ld-relay/issues/406)) ([3c12e10](https://github.com/launchdarkly/ld-relay/commit/3c12e10718b6126c520da259c2b85e015aba05ae))

## [8.7.1](https://github.com/launchdarkly/ld-relay/compare/v8.7.0...v8.7.1) (2024-06-21)


### Bug Fixes

* **deps:** update Dockerfiles from alpine:3.20.0 to alpine:3.20.1 ([#402](https://github.com/launchdarkly/ld-relay/issues/402)) ([f70a5df](https://github.com/launchdarkly/ld-relay/commit/f70a5df9185244893b36a68c18c15cbc89cf4272))

## [8.7.0](https://github.com/launchdarkly/ld-relay/compare/v8.6.0...v8.7.0) (2024-06-17)


### Features

* publish -alpine suffixed Docker images ([#396](https://github.com/launchdarkly/ld-relay/issues/396)) ([2d7cecd](https://github.com/launchdarkly/ld-relay/commit/2d7cecd9242c65207a798404c4a57c6ec3b23961))

## [8.6.0](https://github.com/launchdarkly/ld-relay/compare/v8.5.2...v8.6.0) (2024-06-10)


### Features

* publish distroless debian12 image ([fe0155f](https://github.com/launchdarkly/ld-relay/commit/fe0155f418ca488a98150e6c56a066b1775624d3))

## [8.5.2](https://github.com/launchdarkly/ld-relay/compare/v8.5.1...v8.5.2) (2024-06-05)


### Bug Fixes

* **deps:** bump supported Go versions to 1.22.4 and 1.21.11 ([#372](https://github.com/launchdarkly/ld-relay/issues/372)) ([e0efbad](https://github.com/launchdarkly/ld-relay/commit/e0efbad5ec510e91f360a2bd4e3c0e0b5b91afb1))
* **deps:** update Dockerfiles from alpine:3.19.1 to alpine:3.20.0 ([#386](https://github.com/launchdarkly/ld-relay/issues/386)) ([6935080](https://github.com/launchdarkly/ld-relay/commit/6935080e0852881486de96a927623b1cea62eda1))

## [8.5.1](https://github.com/launchdarkly/ld-relay/compare/v8.5.0...v8.5.1) (2024-05-21)


### Bug Fixes

* **deps:** bump alpine to 3.19.1 ([#366](https://github.com/launchdarkly/ld-relay/issues/366)) ([4def9c4](https://github.com/launchdarkly/ld-relay/commit/4def9c4b41593d88dbb2e1fec00b89ed5a75b950))

## [8.5.0](https://github.com/launchdarkly/ld-relay/compare/v8.4.2...v8.5.0) (2024-05-14)


### Features

* replace offline-mode filewatcher with polling ([#317](https://github.com/launchdarkly/ld-relay/issues/317)) ([7bea824](https://github.com/launchdarkly/ld-relay/commit/7bea824c0d0a5f428174def4db89a3ce9869f0ce))

## [8.4.2](https://github.com/launchdarkly/ld-relay/compare/v8.4.1...v8.4.2) (2024-05-10)


### Bug Fixes

* **deps:** bump golang.org/x/net from 0.19 -&gt; 0.25 for CVE-2023-45288 ([#360](https://github.com/launchdarkly/ld-relay/issues/360)) ([b6eab4c](https://github.com/launchdarkly/ld-relay/commit/b6eab4c56b2753bd172559ec91762f8a3277d6fd))
* **deps:** bump supported Go versions to 1.22.3 and 1.21.10 ([#357](https://github.com/launchdarkly/ld-relay/issues/357)) ([758bfa8](https://github.com/launchdarkly/ld-relay/commit/758bfa809233bd9f31444d3bd28a6795a44f2cca))

## [8.4.1](https://github.com/launchdarkly/ld-relay/compare/v8.4.0...v8.4.1) (2024-04-04)


### Bug Fixes

* **deps:** bump supported Go versions to 1.22.2 and 1.21.9 ([#349](https://github.com/launchdarkly/ld-relay/issues/349)) ([a23f728](https://github.com/launchdarkly/ld-relay/commit/a23f728de5f2597393b607eaa4dc623bea1a1885))

## [8.4.0](https://github.com/launchdarkly/ld-relay/compare/v8.3.2...v8.4.0) (2024-03-14)


### Features

* Support PHP update to event schema v4 ([#522](https://github.com/launchdarkly/ld-relay/issues/522)) ([6d23303](https://github.com/launchdarkly/ld-relay/commit/6d233031b2b9f2e2ca878b3c922f8f85923b5b31))


### Bug Fixes

* Bump go-sdk-events to 3.2.0 ([87e3cc3](https://github.com/launchdarkly/ld-relay/commit/87e3cc37562c28a10a5278c0a603fb5d3a665ee1))
* Bump go-server-sdk/v7 to 7.1.0 ([2cf8613](https://github.com/launchdarkly/ld-relay/commit/2cf8613e224963e5517351b3cc99b33e62a85341))
* **deps:** Bump google.golang.org/protobuf from 1.31.0 to 1.33.0 ([#342](https://github.com/launchdarkly/ld-relay/issues/342)) ([81175da](https://github.com/launchdarkly/ld-relay/commit/81175da78c1d3ea28fc1821de5612d5fee7af3e3))

## [8.3.2](https://github.com/launchdarkly/ld-relay/compare/v8.3.1...v8.3.2) (2024-03-07)


### Bug Fixes

* **deps:** bump supported Go versions to 1.22.1 and 1.21.8 ([#333](https://github.com/launchdarkly/ld-relay/issues/333)) ([4a670b7](https://github.com/launchdarkly/ld-relay/commit/4a670b7037766a2aa50c7e22e56e7d77ee0a3994))

## [8.3.1](https://github.com/launchdarkly/ld-relay/compare/v8.3.0...v8.3.1) (2024-02-12)


### Bug Fixes

* **deps:** bump supported Go versions to 1.22.0 and 1.21.7 ([#314](https://github.com/launchdarkly/ld-relay/issues/314)) ([e81454f](https://github.com/launchdarkly/ld-relay/commit/e81454f097d46f487d870dd1f286e9101fab8bec))

## [8.3.0](https://github.com/launchdarkly/ld-relay/compare/v8.2.3...v8.3.0) (2024-02-09)


### Features

* build Docker images for ARMv7 and ARM64v8 architectures [v8] ([#291](https://github.com/launchdarkly/ld-relay/issues/291)) ([a361688](https://github.com/launchdarkly/ld-relay/commit/a361688d6d8a43feb38464b97e42e9c46d1dca61))

## [8.2.3] - 2024-01-29
### Changed:
- Continuous integration was migrated from CircleCI to Github Actions.
- Bumped supported Go versions from 1.21.5 to 1.21.6, and 1.20.12 to 1.20.13
- Bumped base AWS SDK from 1.18 to 1.24, AWS config module from 1.18 to 1.26, AWS credentials module from 1.13 to 1.16 and AWS dynamodb module from 1.19 to 1.27

### Fixed:
- Offline Mode file watcher should now correctly handle atomic updates to the archive. Thanks, @gmckerrell.

## [8.2.2] - 2024-01-03
### Changed:
- Build with Go 1.21.5 and 1.20.12 in CI
- Bumped Alpine from 3.18 to 3.19
- Bumped go-git from 5.7.0 to 5.11.0 
- Bumped x/crypto from 0.14.0 to 0.17

## [8.2.1] - 2023-11-29
### Changed:
- Bump google.golang.org/grpc from 1.55.0 to 1.56.3
- Bump Alpine from 3.18.3 to 3.18.4
- Bump github.com/docker/docker from v23.0.3 to v24.0.7

## [7.4.2] - 2023-11-29
### Changed:
- Bump google.golang.org/grpc from 1.55.0 to 1.56.3
- Bump Alpine from 3.18.3 to 3.18.4

## [8.2.0] - 2023-10-24
### Added:
- Support sampling and exclusions in PHP events

## [8.1.1] - 2023-10-20
### Fixed:
- The big segments synchronization process will now handle empty versions correctly when using DynamoDB.

## [8.1.0] - 2023-10-17
### Changed:
- Build with Go 1.21 and 1.20 in CI
- Bump minimum required Go version to 1.19

### Removed:
- Removed obsolete documentation related to `disableInternalUsageMetrics` config option.

## [8.0.0] - 2023-10-12
### Added:
- Added support for Payload Filters, which is an EAP feature. Please contact LaunchDarkly support for more information.
- Added support for upcoming technology migration functionality.

### Changed:
- Expanded `relayMetric` to track polling requests.

### Removed:
- `DisableInternalUsageMetrics` configuration has been removed.

## [7.4.1] - 2023-10-20
### Fixed:
- The big segments synchronization process will now handle empty versions correctly when using DynamoDB.

## [7.4.0] - 2023-10-17
### Changed:
- Build with Go 1.21 and 1.20 in CI
- Bump minimum required Go version to 1.19
- Bump golang.org/x/net from 0.11.0 to 0.17.0 


### Deprecated:
- `disableInternalUsageMetrics` is deprecated in Relay Proxy v7 and removed in v8.

## [7.3.3] - 2023-09-27
### Changed:
- Updated internal relay metric to include data scoped to individual environment.
- Bumped cyphar/filepath-securejoin to v0.2.4

## [7.3.2] - 2023-08-08
### Changed:
- Updated Alpine docker image to 3.18.3.

### Fixed:
- Upgrade go from 1.19.9 -> 1.19.12 and 1.20.4 -> 1.20.7 to address [CVE-2023-29402](https://nvd.nist.gov/vuln/detail/CVE-2023-29402), [CVE-2023-29404](https://nvd.nist.gov/vuln/detail/CVE-2023-29404), [CVE-2023-29405](https://nvd.nist.gov/vuln/detail/CVE-2023-29405).

## [7.3.1] - 2023-06-21
### Changed:
- The relay proxy web server will now time out if a connection does not provide header information within 10 seconds.
- Updated Alpine docker image to 3.18.2.

### Fixed:
- Upgrade goreleaser from 2.25.1 -> 2.30 to resolve [CVE-2023-32698](https://nvd.nist.gov/vuln/detail/CVE-2023-32698).

## [7.3.0] - 2023-05-24
### Added:
- Add a version command line option to return Relay's current version

### Fixed:
- Event payloads receiving an HTTP 413 status code will no longer prevent subsequent event payloads from being attempted.
- Unrecoverable failure conditions will no longer block event go routines.

## [7.2.5] - 2023-05-12
### Changed:
- Updated Alpine docker image to 3.18.0.

## [7.2.4] - 2023-05-05
### Fixed:
- Upgrade go from 1.20.2 -> 1.20.4 to address [CVE-2023-24538](https://nvd.nist.gov/vuln/detail/CVE-2023-24538).

## [7.2.3] - 2023-04-13
### Changed:
- Updated Alpine docker image to 3.17.3.
- Bumped docker dependency from 20.10.21+incompatible to 20.10.24+incompatible.

### Fixed: 
- Removed redundant autoconfig stream restart upon receiving reconnect event.

## [7.2.2] - 2023-04-04
### Changed:
- CI tests now execute for Go 1.20.2 and Go 1.19.7.
- Upgraded 3rd party dependencies.

### Fixed:
- Fixed a broken link in configuration.md.

## [7.2.1] - 2023-03-07
### Changed:
- Updated Alpine image to use Go 1.20.1.
- CI tests now execute for Go 1.20.1 and Go 1.19.6; removed Go 1.18.

## [7.2.0] - 2023-02-21
### Changed:
- Updated goreleaser to `v1.15.2`: release artifact names now follow goreleaser's standard `ConventionalFileName` template. Additionally, the deb and rpm packages now install `ld-relay` to `/usr/bin` instead of `/usr/local/bin`.

### Fixed:
- Updated `golang.org/x/net` to `v0.7.0` to address CVE-2022-41723.
- Fixed typo in `docs/metrics.md`.

## [7.1.0] - 2023-02-10
### Added:
- Added ability to specify Redis username via config file or environment variable.

## [7.0.3] - 2023-02-06
### Fixed:
- Fixed Go module path to match the major version number, which was preventing installation using Go tooling. (Thanks, @macro1).

## [7.0.2] - 2023-01-19
### Fixed:
- Removed logging of "Big Segment store status query" error messages in a situation where the Relay Proxy has not been able to synchronize Big Segment data with LaunchDarkly. These messages were redundant since there is already a different and clearer error being logged for the synchronization failure.

## [6.7.18] - 2023-03-06
### Changed:
- Updated Alpine image to use Go 1.20.1.
- CI tests now execute for Go 1.20.1 and Go 1.19.6; removed Go 1.17 and 1.18.

### Fixed:
- Bumped golang.org/x/net to 0.7.0 to address CVE-2022-41723.

## [6.7.17] - 2023-01-17
### Fixed:
- Removed logging of "Big Segment store status query" error messages in a situation where the Relay Proxy has not been able to synchronize Big Segment data with LaunchDarkly. These messages were redundant since there is already a different and clearer error being logged for the synchronization failure.

## [6.7.16] - 2023-01-06
*The 6.7.15 release was incomplete and has been skipped.*

### Changed:
- It is no longer possible to build the Relay Proxy from source code in Go 1.16, or to import it as a module in a Go 1.16 project. This is because the security patch described below is not possible for Go 1.16. Although LaunchDarkly tries to maintain compatibility with old platform versions within the same major version of a LaunchDarkly product, maintaining support for Relay Proxy 6.x requires that we patch security vulnerabilities for the most common use cases; building Relay from source code with an EOL Go version is an uncommon use case.
- Updated Alpine to 3.16.3 in the published Docker image.

### Fixed:
- Updated Go networking code to address CVE-2022-41717. ([#210](https://github.com/launchdarkly/ld-relay/issues/210))

## [7.0.1] - 2022-12-29
### Changed:
- Updated Alpine to 3.16.3 in the published Docker image.

### Fixed:
- Updated Go to 1.19.4 in the published Docker image and executables, to address CVE-2022-41717. ([#210](https://github.com/launchdarkly/ld-relay/issues/210))

### Fixed:
- Updated Go to 1.19.4 in the published Docker image and executables, to address CVE-2022-41717. ([#210](https://github.com/launchdarkly/ld-relay/issues/210))

## [7.0.0] - 2022-12-07
The latest version of the Relay Proxy supports LaunchDarkly's new custom contexts feature. Contexts are an evolution of a previously-existing concept, "users." Contexts let you create targeting rules for feature flags based on a variety of different information, including attributes pertaining to users, organizations, devices, and more. You can even combine contexts to create "multi-contexts." 

This feature is only available to members of LaunchDarkly's Early Access Program (EAP). If you're in the EAP, and the SDK you are using also has an EAP release, you can use contexts by updating your SDK to the latest version and, updating your Relay Proxy. Outdated SDK versions do not support contexts, and will cause unpredictable flag evaluation behavior.

If you are not in the EAP, only use single contexts of kind "user", or continue to use the user type if available. If you try to create contexts, the context will be sent to LaunchDarkly, but any data not related to the user object will be ignored.

For detailed information about this version, please refer to the list below.

### Added:
- Added support for new context-based features in flag evaluations.
- Added evaluation endpoints that are used by new versions of client-side SDKs.

### Changed:
- When building the Relay Proxy from source code or using its packages from application code, the minimum Go version is now 1.18.
- The pre-built binaries and Docker image are now built with Go 1.19.

### Removed:
- Removed support for obsolete evaluation endpoints that were used by very old client-side SDKs.

## [6.7.14] - 2022-10-26
This is a security patch release.

### Fixed:
- Updated Go runtime version in the Docker image to 1.19.2, to address multiple vulnerability reports in Go 1.17.x and 1.18.x. ([#205](https://github.com/launchdarkly/ld-relay/issues/205))
- Updated Consul API module version as a workaround for a false-positive report of CVE-2022-40716. ([#205](https://github.com/launchdarkly/ld-relay/issues/205))
- Removed a transitive dependency on AWS SDK v1, which was causing vulnerability reports for CVE-2020-8911 and CVE-2020-8912; in practice, this functionality was never being used by the Relay Proxy. ([#204](https://github.com/launchdarkly/ld-relay/issues/204))
- Enforce a minimum TLS version of 1.2 when connecting to a secure Redis instance.
- In offline mode, added a check to prevent a maliciously crafted archive file from causing file data to be written outside of the directory where the archive is being expanded.
- Minor code changes to avoid using the deprecated `ioutil` package.
- CI tests now include Go 1.18 and 1.19.

## [6.7.13] - 2022-08-12
### Fixed:
- Updated Alpine version in Docker image to 3.16.2 to address a vulnerability warning. ([#201](https://github.com/launchdarkly/ld-relay/issues/201))

## [6.7.12] - 2022-07-28
### Fixed:
- When using DynamoDB with Big Segments, if the configuration specified a different table name for each environment, that name was being ignored. The Relay Proxy was only respecting the per-environment table name setting for regular data storage, not for Big Segments. This has been fixed so Big Segments data now uses the correct table name. ([#199](https://github.com/launchdarkly/ld-relay/issues/199))

## [6.7.11] - 2022-07-20
### Fixed:
- Updated Alpine version in Docker image to 3.16.1 and patched Go packages to address several vulnerability warnings. ([#197](https://github.com/launchdarkly/ld-relay/issues/197))

## [6.7.10] - 2022-07-12
### Fixed:
- Updated `libcrypto` and `libssl` in the Docker image to address an OpenSSL vulnerability. Although the Relay Proxy does not use OpenSSL (it uses the Go runtime's TLS implementation), our policy is to patch all vulnerabilities detected in the Alpine OS used in our Docker image. ([#195](https://github.com/launchdarkly/ld-relay/issues/195))

## [6.7.9] - 2022-07-01
### Changed:
- If the Relay Proxy receives multiple server-side SDK connections for the same environment at nearly the same time, it can now prepare the flag/segment payload for all of them at once using a single buffer. Previously, a new buffer was always used for each connection, which could cause high transient memory usage if many SDKs connected in rapid succession and if the flag/segment data was large.
(Thanks, [moshegood](https://github.com/launchdarkly/ld-relay/pull/189)!)

## [6.7.8] - 2022-06-13
### Fixed:
- Updated Alpine version to 3.16.0 to address an OpenSSL vulnerability. Although the Relay Proxy does not use OpenSSL (it uses the Go runtime's TLS implementation), our policy is to patch all vulnerabilities detected in the Alpine OS used in our Docker image. ([#191](https://github.com/launchdarkly/ld-relay/issues/191))
- Removed the unnecessary installation of `curl` in the Docker image, which caused security warnings about a vulnerable version of `libcurl` even though it was not being used. ([#191](https://github.com/launchdarkly/ld-relay/issues/191))

## [6.7.7] - 2022-05-10
### Fixed:
- Fixed an inefficiency in the SSE server implementation that could cause unnecessarily large temporary memory usage spikes when the Relay Proxy was sending large flag data sets to server-side SDK clients.

## [6.7.6] - 2022-04-29
### Fixed:
- Setting allowable CORS origin domains with any of the `allowedOrigin`/`ALLOWED_ORIGIN` configuration options did not work correctly: requests with a matching domain would return empty responses. (Thanks, [joshuaeilers](https://github.com/launchdarkly/ld-relay/pull/185)!)

## [6.7.5] - 2022-04-21
### Fixed:
- Updated the `golang.org/x/crypto` package to address CVE-2022-27191. ([#183](https://github.com/launchdarkly/ld-relay/issues/183))

## [6.7.4] - 2022-04-15
### Fixed:
- Updated the Go version for release builds to 1.17.9 to address security warnings about earlier Go runtimes. ([#181](https://github.com/launchdarkly/ld-relay/issues/181))
- Updated the version of the Consul API client due to a vulnerability warning. ([#181](https://github.com/launchdarkly/ld-relay/issues/181))

## [6.7.3] - 2022-04-08
### Fixed:
- When using DynamoDB, if the Relay Proxy attempts to store a feature flag or segment whose total data size is over the 400KB limit for DynamoDB items, it will now log (at `Error` level) a message like `The item "my-flag-key" in "features" was too large to store in DynamoDB and was dropped` but will still process all other data updates. Previously, it would cause the Relay Proxy to enter an error state in which the oversized item would be pointlessly retried and other updates might be lost.
- In flag evaluations for client-side SDKs, fixed an edge case where a circular reference in flag prerequisites (not allowed in a flag configuration, but possible to exist briefly under unlikely circumstances) could have caused a crash. See [Go SDK 5.7.0](https://github.com/launchdarkly/go-server-sdk/releases/tag/5.7.0) release notes.

## [6.7.2] - 2022-04-04
### Fixed:
- Updated Docker image to use Alpine 3.14.6. The previous Alpine version, 3.14.5, was reported to have security vulnerability [CVE-2022-28391](https://nvd.nist.gov/vuln/detail/CVE-2022-28391). See also the [Alpine 3.14.6 changelog](https://git.alpinelinux.org/aports/log/?h=v3.14.6). ([#178](https://github.com/launchdarkly/ld-relay/issues/178))

## [6.7.1] - 2022-03-28
### Fixed:
- Updated Docker image to use Alpine 3.14.5. The previous Alpine version, 3.14.4, was reported to have security vulnerabilities [CVE-2022-0778](https://nvd.nist.gov/vuln/detail/CVE-2022-0778) and [CVE-2018-25032](https://nvd.nist.gov/vuln/detail/CVE-2022-0778). See also the [Alpine 3.14.5 changelog](https://git.alpinelinux.org/aports/log/?h=v3.14.5). ([#175](https://github.com/launchdarkly/ld-relay/issues/175))

## [6.7.0] - 2022-03-24
### Added:
- The Relay Proxy will now forward application metadata information that is included in HTTP headers from any SDKs that have this feature, if the application configures such data.

## [6.6.5] - 2022-03-17
### Fixed:
- Updated Docker image to use Alpine 3.14.4. The previous Alpine version, 3.14.3, was reported to have security vulnerability [CVE-2022-0778](https://nvd.nist.gov/vuln/detail/CVE-2022-0778) in OpenSSL, although the Relay Proxy itself uses Go's implementation of TLS rather than OpenSSL.

## [6.6.4] - 2022-02-07
### Fixed:
- In auto-configuration mode, if the auto-configuration key is invalid, the Relay Proxy should exit with an error code just as it would for other kinds of invalid configuration properties, since there is no way for it to perform any useful functions without having environment information. ([#165](https://github.com/launchdarkly/ld-relay/issues/165))

## [6.6.3] - 2022-01-19
### Changed:
- Updated the Docker image to use Go 1.17.6. The previous Go version, 1.16.10, was reported to have security vulnerabilities [CVE-2021-29923](https://nvd.nist.gov/vuln/detail/CVE-2021-29923) and [CVE-2021-44716](https://nvd.nist.gov/vuln/detail/CVE-2021-44716).

## [6.6.2] - 2022-01-11
### Changed:
- A security scan with [Trivy](https://aquasecurity.github.io/trivy) is now included in every CI build, including both the compiled Relay Proxy executable and the Docker image.

### Fixed:
- Updated dependency `golang.org/x/text` to v0.3.7 due to vulnerability [GO-2021-0113](https://osv.dev/vulnerability/GO-2021-0113).

## [6.6.1] - 2022-01-06
### Fixed:
- The Relay Proxy status resource could incorrectly report the overall status as "degraded" if a database was enabled, even if everything was working normally. This was due to incorrectly assuming that Big Segments data would be present, when the Big Segments feature was not being used. This has been fixed so that now if there are no Big Segments, the status resource does not include a `bigSegmentStatus` property and the overall status is calculated correctly.

## [6.6.0] - 2022-01-05
### Added:
- New configuration property `clientSideBaseURI` (environment variable `CLIENT_SIDE_BASE_URI`) for unusual cases where a custom domain is being used specifically for client-side SDK polling requests. This and other base URI options will never need to be set by most users.

### Changed:
- If the base URI properties are not overridden with custom settings, the Relay Proxy now uses the hostnames `sdk.launchdarkly.com` and `clientsdk.launchdarkly.com` instead of `app.launchdarkly.com` when making requests to certain LaunchDarkly endpoints. This has no effect on the Relay Proxy's functionality, but allows LaunchDarkly's load-balancing behavior to work more efficiently.

## [6.5.2] - 2021-11-19
### Changed:
- Building the Relay Proxy from source code now requires Go 1.16 or higher.

### Fixed:
- Queries for Big Segment data were failing if `BaseURI` had not been explicitly set in the configuration. This error would appear in the log as "BigSegmentSynchronizer: Synchronization failed ... unsupported protocol scheme".
- Updated dependencies to remove a transitive dependency on `jwt-go`. This had previously required a `replace` directive as a workaround (https://github.com/launchdarkly/ld-relay/issues/150), which is no longer necessary.
- Updated the `golang.org/x/crypto` package to a newer version that does not have the vulnerability [CVE-2020-29652](https://nvd.nist.gov/vuln/detail/CVE-2020-29652). Practically speaking this was not a vulnerability in the Relay Proxy, because the potential attack involved a feature of that package that the Relay Proxy does not use (SSH).

## [6.5.1] - 2021-11-16
### Fixed:
- Updated the Go runtime version in the Docker image to 1.16.10. This addresses known security issues in earlier versions of Go as reported in the CVE database.
- Updated the Alpine version in the Docker image to 3.14.3. See [Alpine 3.14.3 release notes](https://www.alpinelinux.org/posts/Alpine-3.14.3-released.html) regarding issues addressed in this release.
- Updated the dependency version of `github.com/hashicorp/consul/api` to v1.11.0. This was to address vulnerabilities that have been reported against earlier versions of Consul. We believe that those CVE reports are somewhat misleading since they refer to the Consul _server_, rather than the API library, but vulnerability scanners often conflate the two and the only known workaround is to update the API version (see https://github.com/hashicorp/consul/issues/10674).

## [6.5.0] - 2021-10-11
### Added:
- It is now possible to add custom values for the `Access-Control-Allow-Headers` header that the Relay Proxy returns for cross-origin requests from browser clients, using a new [per-environment configuration option](https://github.com/launchdarkly/ld-relay/blob/v6/docs/configuration.md#file-section-offlinemode) `allowedHeader` or `$LD_ALLOWED_HEADER_EnvName`. This might be necessary to avoid cross-origin requests being rejected if you have an Internet gateway that uses a custom header for authentication.

## [6.4.5] - 2021-10-08
### Fixed:
- The options for setting allowable CORS origins for browser requests (`allowedOrigin`/`LD_ALLOWED_ORIGIN_envname`, etc.) were being ignored.

## [6.4.4] - 2021-10-05
### Fixed:
- Updated Docker base image to [Alpine 3.14.2](https://alpinelinux.org/posts/Alpine-3.14.2-released.html), to fix `openssl` vulnerabilities CVE-2021-3711 and CVE-2021-3712.

## [6.4.3] - 2021-09-22
### Fixed:
- The Redis password and Redis TLS options, when set as separate configuration variables rather than as part of the Redis URL, did not work when using Redis for Big Segment data. This could also cause misleading log warnings even if Big Segments were not being used.
- When using Redis, if the Redis URL contains a password, the password is now replaced with `xxxxx` in log messages and in the Relay Proxy status resource.

## [6.4.2] - 2021-08-24
### Fixed:
- When using [big segments](https://docs.launchdarkly.com/home/users/big-segments), the Relay Proxy was not correctly notifying already-connected client-side SDKs (mobile or browser apps) to get updated flag values if there was a live update to a big segment.

## [6.4.1] - 2021-07-29
### Fixed:
- Updated a dependency to address a vulnerability ([CVE-2020-26160](https://www.whitesourcesoftware.com/vulnerability-database/CVE-2020-26160)) in a module used by the Prometheus metrics integration. ([#150](https://github.com/launchdarkly/ld-relay/issues/150))
- Fixed a problem (introduced in v6.4.0) that was causing Relay Proxy to log error messages if Redis or DynamoDB was enabled and you were _not_ using the new big segments feature.
- When using big segments, log messages related to the processing of big segment data were hard to interpret because they did not indicate which environment they were for. Now, they have an environment name or ID prefix like other Relay Proxy log messages that are environment-specific.

## [6.4.0] - 2021-07-22
### Added:
- The Relay Proxy now supports evaluation of Big Segments. An Early Access Program for creating and syncing Big Segments from customer data platforms is available to enterprise customers.

## [6.3.1] - 2021-07-08
### Fixed:
- The base OS image for the Docker image has been changed from `alpine:3.12.0` to `alpine:3.14.0`, the latest stable version of Alpine. This fixes known vulnerabilities in Alpine 3.12.0 ([here](https://snyk.io/test/docker/alpine%3A3.12.0) is one list of them). There are no changes to the Relay Proxy itself in this release.

## [6.3.0] - 2021-06-17
### Added:
- The internal SDK logic used in evaluating flags for client-side SDKs now supports the ability to control the proportion of traffic allocation to an experiment. This works in conjunction with a new platform feature now available to early access customers.

## [6.2.2] - 2021-06-07
### Changed:
- Updated the third-party packages that provide metrics integration for DataDog, Prometheus, and StackDriver to their latest releases. These are the latest versions of the OpenCensus integrations; the Relay Proxy is not yet using OpenTelemetry, the newer API that will replace OpenCensus. This update does not fix any known bugs or add any new metrics capabilities to the Relay Proxy; its purpose is to fix potential dependency conflicts in the build.

## [6.2.1] - 2021-06-03
### Fixed:
- Fixed a bug in JSON parsing that could cause floating-point numbers (in flag variation values, rule values, or user attributes) to be read incorrectly if the number format included an exponent and did not include a decimal point (for instance, `1e5`). Since there are several equally valid number formats in JSON (so `1e5` is exactly equivalent to `100000`), whether this bug showed up would depend on the format chosen by whatever software had most recently converted the number to JSON before it was re-read, which is hard to predict, but it would only be likely to happen with either integers that had more than four trailing zeroes or floating-point numbers with leading zeroes. This bug also existed in the LaunchDarkly Go SDK prior to version 5.3.1, so anyone who uses both the Relay Proxy and the Go SDK should update both.

## [6.2.0] - 2021-05-07
### Added:
- New [configuration option](https://github.com/launchdarkly/ld-relay/blob/v6/docs/configuration.md#file-section-main) `InitTimeout`, controlling how long the Relay Proxy will wait for its initial connection to LaunchDarkly; this was previously always 10 seconds.

## [6.1.7] - 2021-04-30
### Added:
- In the [&#34;Proxy mode&#34;](https://github.com/launchdarkly/ld-relay/blob/v6/docs/proxy-mode.md) and [&#34;Daemon mode&#34;](https://github.com/launchdarkly/ld-relay/blob/v6/docs/daemon-mode.md) documentation pages, there is now a description of how the Relay Proxy behaves if it can&#39;t connect to LaunchDarkly, or if there is an invalid SDK key.

### Fixed:
- _(This bug was previously thought to have been fixed in the 6.1.3 release, but it was not fixed then; it is now.)_ During startup, if a timeout occurs while waiting for data from LaunchDarkly, the Relay Proxy endpoints for that environment should accept requests even though the environment is not fully initialized yet, using any last known flag data that might be available (that is, in a database). That is consistent with the behavior of the server-side SDKs, and it was the behavior of the Relay Proxy prior to the 6.0 release but it had been accidentally changed so that the endpoints continued to return 503 errors in this case.

## [6.1.6] - 2021-04-23
### Fixed:
- In automatic configuration mode, if a new credential was created for an already-configured environment (for instance, by using &#34;reset SDK key&#34; on the LaunchDarkly dashboard), the Relay Proxy did not accept subsequent SDK requests using the new credential until the next time the Relay Proxy was started. The symptom was that the SDK endpoints would return a 404 status instead of 200 (even though unknown credentials normally receive a 401). Also, if an SDK key or mobile key had already been changed prior to starting the Relay Proxy, but the older key had not yet expired, the Relay Proxy was not recognizing the _old_ key in requests. Both of these problems have been fixed.
- Fixed a broken link in the Metrics documentation page. (Thanks, [natashanwright](https://github.com/launchdarkly/ld-relay/pull/135)!)

## [6.1.5] - 2021-02-04
### Changed:
- Updated the AWS SDK version used for DynamoDB access to 1.37.2. Among other improvements as described in the [AWS Go SDK release notes](https://github.com/aws/aws-sdk-go/blob/master/CHANGELOG.md), this allows it to support [IAM roles for service accounts](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts-minimum-sdk.html). ([#127](https://github.com/launchdarkly/ld-relay/issues/127))

## [6.1.4] - 2021-01-21
### Fixed:
- Incorporated the fix in version [5.1.3](https://github.com/launchdarkly/go-server-sdk/releases/tag/5.1.3) of the LaunchDarkly Go SDK for feature flags that use semantic version operators. This affects client-side SDKs that connect to the Relay Proxy to evaluate flags.
- A bug was introduced in version 6.1.3 causing an Info-level log message `got put: {DATA}` to be logged upon making a stream connection to LaunchDarkly, where `{DATA}` was the JSON representation of all of the feature flag data received from LaunchDarkly. This has been removed.

## [6.1.3] - 2021-01-21
### Changed:
- The Relay Proxy now uses a more efficient JSON reading and writing mechanism instead of Go&#39;s `encoding/json` when it is reading feature flag data from LaunchDarkly, and when it is creating JSON responses for SDK endpoints. Both CPU usage and the number of memory allocations have been greatly decreased for these operations. How much of a performance improvement this represents in the real world for any given Relay Proxy instance will depend on how often these operations are being done, that is, how often there are flag updates from LaunchDarkly and/or requests from SDK clients.
- When proxying events from the PHP SDK, the Relay Proxy now supports a new type of event, &#34;alias&#34;, which will be implemented in a future release of the PHP SDK.

### Fixed:
- During startup, if a timeout occurs while waiting for data from LaunchDarkly, the Relay Proxy endpoints for that environment will stop returning 503 errors (as they do while waiting for initialization), and start accepting requests even though the environment is not fully initialized yet—in case there is any last-known data (that is, in a database) from an earlier successful startup. This is the standard behavior of the server-side SDKs, and it was the behavior of the Relay Proxy prior to the 6.0 release but had been accidentally changed.
- Corrected and clarified documentation pages &#34;[Metrics](https://github.com/launchdarkly/ld-relay/blob/v6/docs/metrics.md)&#34; and &#34;[Service endpoints](https://github.com/launchdarkly/ld-relay/blob/v6/docs/endpoints.md)&#34;.

## [6.1.2] - 2020-12-04
### Fixed:
- In a load-balanced configuration where multiple Relay Proxy instances were sharing a single database for their own persistent storage, but SDK clients were connecting to the Relay Proxy streaming endpoints via HTTP instead of reading from the database, it was possible for some of the Relay Proxy instances to fail to transmit a feature flag update from LaunchDarkly to the SDK clients (because they would see that another Relay Proxy instance had already updated the database, and would incorrectly assume that this meant clients already knew about it). This problem was introduced in version 6.0.0 and is fixed in this release.

## [6.1.1] - 2020-12-04
### Fixed:
- In [automatic configuration mode](https://docs.launchdarkly.com/home/advanced/relay-proxy-enterprise/automatic-configuration), if an environment&#39;s SDK key was changed but the old key had not yet expired, the Relay Proxy did not accept client requests with the old key as it should have. ([#112](https://github.com/launchdarkly/ld-relay/issues/112))
- In automatic configuration mode, if there was a delay in obtaining the initial configuration from LaunchDarkly (due to network latency or service latency), and the Relay Proxy received client requests with valid SDK keys during that interval, it would return 401 errors because it did not yet know about those SDK keys. This has been fixed so it will return a 503 status if it does not yet have a full configuration. ([#113](https://github.com/launchdarkly/ld-relay/issues/113))
- In [offline mode](https://docs.launchdarkly.com/home/advanced/relay-proxy-enterprise/offline), the Relay Proxy could still try to send analytics events to LaunchDarkly if you explicitly set `sendEvents = true`. Now, it will never send events to LaunchDarkly in offline mode, but enabling `sendEvents` will still cause the Relay Proxy to accept events, so that if SDK clients try to send events they will not get errors; the events will be discarded.
- In offline mode, the Relay Proxy would still try to send internal usage metrics unless you explicitly set `disableInternalUsageMetrics = true`. Now, enabling offline mode automatically disables internal usage metrics.
- Added a note in [documentation](https://github.com/launchdarkly/ld-relay/blob/v6/docs/persistent-storage.md) to clarify that the Relay Proxy does not currently support clustered Redis or Redis Sentinel.
- Fixed broken images in documentation.


## [6.1.0] - 2020-11-09
### Added:
- The Relay Proxy now supports a new &#34;offline mode&#34; which is available to customers on LaunchDarkly&#39;s Enterprise plan. This mode allows the Relay Proxy to run without ever connecting it to LaunchDarkly. When running in offline mode, the Relay Proxy gets flag and segment values from an archive on your filesystem, instead of contacting LaunchDarkly&#39;s servers. To learn more, read [the online documentation](https://docs.launchdarkly.com/home/advanced/relay-proxy-enterprise/offline).

### Fixed:
- The contribution guidelines incorrectly indicated the minimum Go version as 1.13 instead of 1.14.

## [6.0.3] - 2020-10-26
### Fixed:
- Fixed a dependency path that could cause a compiler error (`code in directory $GOPATH/src/github.com/go-gcfg/gcfg expects import "gopkg.in/gcfg.v1"`) when building the Relay Proxy from source code in some cases.

## [6.0.2] - 2020-10-20
### Fixed:
- If a flag or segment was deleted in LaunchDarkly after the Relay Proxy started up, SDK clients that connected to Relay Proxy endpoints after that point could receive an unexpected null value for that flag or segment in the JSON data. This would cause an error in some SDKs causing their stream connections to stop working. This bug was introduced in version 6.0.0.
- When forwarding events from a PHP SDK, the Relay Proxy might omit information about private user attributes (that is, the existence of the attribute would be lost; it would not become non-private). This bug was introduced in version 6.0.0.
- In automatic configuration mode, there was a memory leak when a previously active environment was removed from the configuration: the Relay Proxy could fail to dispose of the in-memory data and worker goroutine(s) related to that environment.

## [6.0.1] - 2020-10-08
### Fixed:
- When sending flag/segment JSON data to SDKs or storing it in a database, properties with default values (such as false booleans or empty arrays) were being dropped entirely to save bandwidth. However, some of the LaunchDarkly SDKs do not tolerate missing properties, so this has been fixed to remain consistent with the less efficient behavior of previous Relay Proxy and Go SDK versions.
- When using automatic configuration mode, under some circumstances the Relay Proxy might make an unnecessary attempt to contact LaunchDarkly using an expired SDK key, which would fail. This did not affect use of the current SDK key, but it would cause a misleading error message in the log.

## [6.0.0] - 2020-10-07
For more details on changes related to configuration, read the [configuration documentation](https://github.com/launchdarkly/ld-relay/blob/v6/docs/configuration.md).

### Added:
- The Relay Proxy now supports a new mode named &#34;automatic configuration&#34; which is available to customers on LaunchDarkly&#39;s Enterprise plan. This mode allows environments and their credentials can be configured dynamically rather than having to be manually configured ahead of time. To learn more, read [the online documentation](https://docs.launchdarkly.com/home/advanced/relay-proxy/automatic-configuration).
- Secure mode can be enabled for an environment by setting `SecureMode = true` for that environment in the configuration file, or `LD_SECURE_MODE_MyEnvName` if using environment variables. This is separate from the setting for secure mode on the LaunchDarkly dashboard, which the Relay Proxy is not able to access.
- The new `DisconnectedStatusTime` configuration property controls how long the Relay Proxy will tolerate a stream connection being interrupted before reporting a &#34;disconnected&#34;/&#34;degraded&#34; status in the [status resource](https://github.com/launchdarkly/ld-relay/blob/v6/docs/endpoints.md).
- The new `MaxClientConnectionTime` configuration property can make the Relay Proxy drop client connections automatically after some amount of time, to improve load balancing. ([#92](https://github.com/launchdarkly/ld-relay/issues/92))
- The Consul integration now supports ACL tokens with the `Token` and `TokenFile` configuration properties.
- The new `DisableInternalUsageMetrics` configuration property allows turning off the internal analytics that the Relay Proxy normally sends to LaunchDarkly.
- The `/status` endpoint now includes more information about the LaunchDarkly connection status, database connection status (if applicable) and database configuration (if applicable). To learn more, read [Service endpoints](https://github.com/launchdarkly/ld-relay/blob/v6/docs/endpoints.md). ([#104](https://github.com/launchdarkly/ld-relay/issues/104))

### Changed (breaking changes in configuration):
- Relay will now print an error and refuse to start if there is any property name or section name in a configuration file that it does not recognize. Previously, it would print a warning but then continue. This change was made because otherwise it is easy to misspell a property and not notice that the value is not being used.
- All configuration settings that represent a time duration must now be specified in a format that includes units, such as `3s` for 3 seconds or `5m` for 5 minutes. The affected settings include `[Main] HeartbeatInterval` (`HEARTBEAT_INTERVAL`), `[Events] FlushInterval` (`EVENTS_FLUSH_INTERVAL`), `[any database] LocalTTL` (`CACHE_TTL`), and `[any environment] TTL` (`LD_TTL_envname`).
- Previously, environment variables that have a true/false value were assumed to be false if the value was anything other than `true` or `1`. Now, any value other than `true`, `false`, `0`, or `1` is an error.
- All configuration settings that represent port numbers now cause an error if you set them to zero or a negative number. The same is true of the event capacity setting.
- The environment variable name `LD_TTL_MINUTES_envname` is no longer supported. Use `LD_TTL_envname` instead.
- The environment variable name `REDIS_TTL` is no longer supported. Use `CACHE_TTL` instead.
- The setting `[Events] SamplingInterval` (`SAMPLING_INTERVAL`) is no longer supported.

### Changed (breaking changes when building the Relay Proxy):
- When building the Relay Proxy, you must use Go 1.14 or higher and [Go modules](https://github.com/golang/go/wiki/Modules).
- The build command is now just `go build`, rather than `go build ./cmd/ld-relay`.

### Changed (breaking changes when using the Relay Proxy as a library):
- When using the Relay Proxy as a library, you must use Go 1.14 or higher and [Go modules](https://github.com/golang/go/wiki/Modules).
- The base import path is now `github.com/launchdarkly/ld-relay/v6` instead of `gopkg.in/launchdarkly/ld-relay.v5`.
- The import path for the `Relay` type is now `github.com/launchdarkly/ld-relay/v6/relay`.
- The `Config` structs are now in a `config` subpackage. The types of many fields have changed to types that prevent creating a configuration with invalid values (for instance, `OptURLAbsolute` for URL fields instead of `string`).
- There is no longer a `DefaultConfig`. Instead, Relay automatically uses the appropriate default values for any configuration fields that are not set.

### Changed (other):
- The prebuilt binaries for Relay Proxy releases no longer include a 32-bit Darwin/MacOS version; current versions of Go only support 64-bit for Darwin. The Linux binaries still include both 32-bit and 64-bit.
- The documentation in the source code repository has been reorganized into multiple files for clarity, so `README.md` is now only a summary with links to the other files.

### Removed:
- The undocumented `InsecureSkipVerify` configuration property has been removed.

## [5.12.2] - 2020-09-23
### Fixed:
- The prebuilt Linux Docker image contained version 1.1.1c of `openssl`, which had several known security vulnerabilities. The image now contains version 1.1.1g of `openssl`, and also uses the more current 3.12.0 version of the Alpine Linux distribution rather than 3.10.2.
- Fixed instabilities in the CI build.


## [5.12.1] - 2020-08-10
### Fixed:
- The configuration section of `README.md` mistakenly referred to `MinTlsVersion` and `MIN_TLS_VERSION` as `MinTlsLevel` and `MIN_TLS_LEVEL`. This release simply fixes the documentation error; the behavior of Relay Proxy has not changed.

## [5.12.0] - 2020-08-06
### Added:
- There is a new configuration option for specifying the lowest allowable TLS version, when using the Relay Proxy as a secure server. This is `MinTlsVersion` in the `[Main]` section of the configuration file, or the variable `MIN_TLS_VERSION` if using environment variables. For instance, setting this option to `1.2` means that all requests from clients must use TLS 1.2 or higher.

## [5.11.2] - 2020-08-06
### Changed:
- There is a `clientSideAvailability` property which will be sent by LaunchDarkly services in the future as an alternate way of indicating whether a flag is enabled for use by client-side/mobile JavaScript SDKs. Previous versions of the Relay Proxy did not support this property, so the more detailed availability features being added to the LaunchDarkly dashboard would not work for applications that connected through a Relay Proxy. This version adds that support.

## [5.11.1] - 2020-07-07
### Changed:
- Updated the README to have updated usage guidance and to fix outdated links.

### Fixed:
- When proxying events, events that were received from JavaScript browser clients via the special image-loading endpoint (used if the browser does not support CORS) could be lost.
- When Prometheus metrics are enabled, the `/metrics` endpoint that Relay provides for the Prometheus agent to query was being added globally using `http.Handle`; that meant that if an application used Relay as a library, and used `http.ListenAndServe` with the default handler, it would have a `/metrics` endpoint on its own port. This has been fixed so the endpoint is only defined on the Prometheus exporter&#39;s port.
- Stream reconnections now use a backoff delay with jitter, instead of a fixed delay.


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
