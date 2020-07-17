# LaunchDarkly Relay Proxy - Client-Side/Mobile Connections

[(back to README)](../README.md)

When using [proxy mode](./proxy-mode.md) and/or [event forwarding](events.md), by default the Relay Proxy only accepts requests from server-side SDKs, which are authorized with an SDK key.

If you want it to also accept requests from mobile SDKs and/or client-side JavaScript SDKs, you must provide the corresponding credentials (mobile key, client-side environment ID) in your [configuration](./configuration.md#file-section-environment-name). You will then need to configure the `baseUri` and `streamUri` properties in your SDKs to point to the location of the Relay Proxy

In these examples, one environment allows only mobile and another allows only client-side JavaScript, but you could also have an environment that uses both.

## Configuration file example

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

If you are enabling access by client-side JavaScript SDKs by setting `envId`, you can also specify that only requests from specific web sites should be allowed. This value will be provided by the Relay Proxy in the [`Access-Control-Allow-Origin`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Access-Control-Allow-Origin) HTTP header for cross-origin browser requests.

```
# Specifying allowed origins in a configuration file
[Environment "B"]
    sdkKey = "the SDK key for environment B"
    envId = "the client-side environment ID for environment B"
    allowedOrigin = "http://example.org"
    allowedOrigin = "http://another_example.net"
```

```
# Specifying allowed origins with environment variables
LD_ENV_B=the SDK key for environment B
LD_CLIENT_SIDE_ID_B=the client-side environment ID for environment B
LD_ALLOWED_ORIGIN_B=http://example.org,http://another_example.net
```

Also, if you are exposing any of the client-side relay endpoints externally, it is highly recommended to use HTTPS-- either by configuring the Relay Proxy itself to be a secure server, or by placing an HTTPS proxy server in front of it rather than exposing the Relay Proxy directly. See: [Using TLS](./tls.md)
