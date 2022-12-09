# LaunchDarkly Relay Proxy - Relativity security-hardened build

A custom Relativity build process for the [Launch Darkly Relay Proxy](https://docs.launchdarkly.com/home/relay-proxy). The repository imports [`launchdarkly/ld-relay`](https://github.com/launchdarkly/ld-relay) as a Git submodule. The GitHub Action pipeline builds a container image quite similar to the official one, except using Relativity's hardened `r1/base/security-alpine3` as base.

## Maintainers

The [Mighty Configurator](https://einstein.kcura.com/x/TgAyCw) team.