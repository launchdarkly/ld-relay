# LaunchDarkly Relay Proxy - Using with Docker

[(Back to README)](../README.md)

Using Docker is not required, but if you prefer using a Docker container we provide a Docker entrypoint to make this as easy as possible.

To build the `ld-relay` container:
```
$ docker build -t ld-relay .
```

In Docker, the config file is expected to be found at `/ldr/ld-relay.conf`, unless you are using environment variables to configure the Relay Proxy. To learn more, read [Configuration](./configuration.md).

## Docker examples

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
