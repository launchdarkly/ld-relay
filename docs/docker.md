# LaunchDarkly Relay Proxy - Using with Docker

[(Back to README)](../README.md)

Using Docker is not required, but if you prefer using a Docker container we provide a Docker entrypoint to make this as easy as possible.

We provide images based on Google's ["distroless"](https://github.com/GoogleContainerTools/distroless) Debian12
images. Because Relay Proxy is a statically linked Go binary, it can take advantage of the reduced dependencies in the
Distroless base images.

| Image              | Version                                                                                                                       | Size                                                                                                                                                          | Platforms              |
|--------------------|-------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------|
| Distroless         | [![Docker Image Version](https://img.shields.io/docker/v/launchdarkly/ld-relay/latest-static-debian12-nonroot)][dockerhub-distroless]             | [![Docker Image Size (tag)](https://img.shields.io/docker/image-size/launchdarkly/ld-relay/latest-static-debian12-nonroot)][dockerhub-distroless]             | amd64, armv7, arm64v8 |
| Distroless (debug) | [![Docker Image Version](https://img.shields.io/docker/v/launchdarkly/ld-relay/latest-static-debian12-debug-nonroot)][dockerhub-distroless-debug] | [![Docker Image Size (tag)](https://img.shields.io/docker/image-size/launchdarkly/ld-relay/latest-static-debian12-debug-nonroot)][dockerhub-distroless-debug] | amd64, armv7, arm64v8 |

The tags without a suffix (`latest`, `v9`, and `9.x.y`) point at the Distroless image. They are the same as the
`-static-debian12-nonroot` tags.

### Changes from Relay Proxy v8

Relay Proxy v8 and earlier also published an Alpine Linux image, and the tags without a suffix pointed at it. Relay
Proxy v9 does not publish an Alpine image or an i386 image. If you move from an Alpine image, note these differences:

* The image does not contain a shell or utilities such as `wget`. Health checks and probes that run a command in the
  container do not work. Use an HTTP health check on the `/status` endpoint instead. The debug variant contains a
  busybox shell at `/busybox/sh`.
* The image runs as the `nonroot` user (UID 65532) instead of UID 1000. Make sure that this user can read files that
  you mount into the container, such as the configuration file.
* The image does not contain a package manager. Images that you build `FROM launchdarkly/ld-relay` cannot use `apk`.
* For i386 systems, use the `linux_386` archive or the i386 deb and rpm packages from the
  [GitHub releases](https://github.com/launchdarkly/ld-relay/releases).

## Local Development

When developing locally, you can build the `ld-relay` container with the following command:
```shell
$ docker build -t ld-relay .
```

Please note that this [Dockerfile](../Dockerfile) is **not** the same one that is published to 
[DockerHub](https://hub.docker.com/r/launchdarkly/ld-relay).

It is a convenience for local development, whereas the images published to DockerHub are built during our release 
process and are based on [Dockerfile-static-debian12-nonroot.goreleaser](../Dockerfile-static-debian12-nonroot.goreleaser)
and [Dockerfile-static-debian12-debug-nonroot.goreleaser](../Dockerfile-static-debian12-debug-nonroot.goreleaser).

In Docker, the config file is expected to be found at `/ldr/ld-relay.conf`, unless you are using environment variables 
to configure the Relay Proxy. To learn more, read [Configuration](./configuration.md).

## Local Development Examples

To run a single environment, without Redis:
```shell
$ docker run --name ld-relay -e LD_ENV_test="sdk-test-sdkKey" ld-relay
```

To run multiple environments, without Redis:
```shell
$ docker run --name ld-relay -e LD_ENV_test="sdk-test-sdkKey" -e LD_ENV_prod="sdk-prod-sdkKey" ld-relay
```

To run a single environment, with Redis:
```shell
$ docker run --name redis redis:alpine
$ docker run --name ld-relay --link redis:redis -e USE_REDIS=1 -e LD_ENV_test="sdk-test-sdkKey" ld-relay
```

To run multiple environment, with Redis:
```shell
$ docker run --name redis redis:alpine
$ docker run --name ld-relay --link redis:redis -e USE_REDIS=1 -e LD_ENV_test="sdk-test-sdkKey" -e LD_PREFIX_test="ld:default:test" -e LD_ENV_prod="sdk-prod-sdkKey" -e LD_PREFIX_prod="ld:default:prod" ld-relay
```

## Production Deployment

Please note that the default Distroless image does not contain a shell. 

### Distroless Variants

Relay's Distroless images are distributed in two variants. The first is intended for regular usage, while the 
second is for debugging and contains a shell.

| Docker image tag suffix           | Based on [Distroless](https://github.com/GoogleContainerTools/distroless) tag.. | Purpose                  |
|-----------------------------------|---------------------------------------------------------------------------------|--------------------------|
| `-static-debian12-nonroot`        | `static-debian12:nonroot`                                                       | Normal usage             |
| `-static-debian12-debug-nonroot-` | `static-debian12:debug-nonroot`                                                 | Contains a busybox shell |

To enter the busybox shell for debugging purposes on a running container (only available in the `-debug-nonroot` 
variant):
```shell
docker exec -it [container name] /busybox/sh
```

[dockerhub-distroless]: https://hub.docker.com/r/launchdarkly/ld-relay/tags?page=&page_size=&ordering=&name=static-debian12-nonroot
[dockerhub-distroless-debug]: https://hub.docker.com/r/launchdarkly/ld-relay/tags?page=&page_size=&ordering=&name=static-debian12-debug-nonroot

