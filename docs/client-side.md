# LaunchDarkly Relay Proxy - Client-side and mobile connections

[(Return to the README)](../README.md)

By default, when the Relay Proxy is in [proxy mode](./proxy-mode.md) or is using [event forwarding](./events.md), it only accepts requests from server-side SDKs which are authorized with an SDK key.

If you want the Relay Proxy to also accept requests from mobile SDKs or client-side JavaScript SDKs, you must provide the corresponding credentials of either a mobile key or client-side environment ID in your [configuration](./configuration.md#file-section-environment-name). You must configure the `baseUri` and `streamUri` properties in your SDKs to point to the location of the Relay Proxy. To learn more, read [Service endpoint configuration](https://docs.launchdarkly.com/sdk/features/service-endpoint-configuration).

In these examples, one environment allows only mobile and another allows only client-side JavaScript, but you could also have an environment that uses both.

## Configuration file example

Here are examples of configuration files that support different kinds of SDKs:

```
# This environment supports only server-side and mobile SDKs
[Environment "A"]
    sdkKey = "the SDK key for environment A"
    mobileKey = "the mobile key for environment A"

# This environment supports only server-side and JavaScript client-side SDKs
[Environment "B"]
    sdkKey = "the SDK key for environment B"
    envId = "the client-side environment ID for environment B"

# This environment supports all three
[Environment "C"]
    sdkKey = "the SDK key for environment C"
    mobileKey = "the mobile key for environment C"
    envId = "the client-side environment ID for environment C"
```

## Environment variables example

Here are examples of environment variables that support different kinds of SDKs:

```
# This environment supports only server-side and mobile SDKs
LD_ENV_A=the SDK key for environment A
LD_MOBILE_KEY_A=the mobile key for environment A

# This environment supports only server-side and JavaScript client-side SDKs
LD_ENV_B=the SDK key for environment B
LD_CLIENT_SIDE_ID_B=the client-side environment ID for environment B

# This environment supports all three
LD_ENV_C=the SDK key for environment C
LD_CLIENT_SIDE_ID_C=the client-side environment ID for environment C
```

## Access control for JavaScript client-side use

If you enable access by client-side JavaScript SDKs by setting `envId`, you can also specify that only requests from specific web sites should be allowed. The Relay Proxy provides this value in the [`Access-Control-Allow-Origin`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Access-Control-Allow-Origin) HTTP header for cross-origin browser requests.

If you need to allow extra headers to pass through during cross-origin browser requests, you can specify which headers should be allowed. The Relay Proxy provides this value in the [`Access-Control-Allow-Headers`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Access-Control-Allow-Headers) HTTP header for cross-origin browser requests.

```
# Specifying allowed origins in a configuration file
[Environment "B"]
    sdkKey = "the SDK key for environment B"
    envId = "the client-side environment ID for environment B"
    allowedOrigin = "http://example.org"
    allowedOrigin = "http://another_example.net"
    allowedHeader = "Timestamp"
    allowedHeader = "Company-A-Identifier"
```

```
# Specifying allowed origins with environment variables
LD_ENV_B=the SDK key for environment B
LD_CLIENT_SIDE_ID_B=the client-side environment ID for environment B
LD_ALLOWED_ORIGIN_B=http://example.org,http://another_example.net
LD_ALLOWED_HEADER_B=Timestamp,Company-A-Identifier
```

If you expose any of the client-side relay endpoints externally, we strongly recommend that you use HTTPS, either by configuring the Relay Proxy itself to be a secure server, or by placing an HTTPS proxy server in front of it, rather than exposing the Relay Proxy directly. To learn more, read [Using TLS](./tls.md).
