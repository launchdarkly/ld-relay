# LaunchDarkly Relay Proxy - Using TLS

[(Back to README)](../README.md)

There are two ways to ensure that SDKs communicate securely with the Relay Proxy when using [proxy mode](./proxy-mode.md).

The first option is to place a standard reverse proxy in front of the Relay Proxy. Configure it to only accept secure connections, and to forward all requests to the Relay Proxy. Then configure the SDKs to point to this host instead of directly to the Relay Proxy.

The second option is to make the Relay Proxy itself into a secure server by turning on the `tlsEnabled` configuration file option or the `TLS_ENABLED` environment variable. Optionally, you can specify a custom server certificate and key. To learn more, read [Configuration](./configuration.md#file-section-main).

The Relay Proxy does not support every possible TLS configuration option for secure servers, such as enabling only certain TLS ciphers. You can have more control over the configuration if you use a full-featured reverse proxy as described above.
