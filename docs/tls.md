# LaunchDarkly Relay Proxy - Using TLS

[(back to README)](../README.md)

There are two ways to ensure that SDKs communicate securely with the Relay Proxy, when using [proxy mode](./proxy-mode.md).

One is to place a standard reverse proxy in front of the Relay Proxy. Configure it to only accept secure connections, and to forward all requests to the Relay Proxy. Then, configure the SDKs to point to this host instead of directly to the Relay Proxy.

The other is to make the Relay Proxy itself into a secure server, by turning on the `tlsEnabled` configuration file option or the `TLS_ENABLED` environment variable. You may optionally specify a custom server certificate and key. For more details, see [Configuration](./configuration.md#file-section-main).

Note that the Relay Proxy does not support every possible TLS configuration option for secure servers, such as enabling only certain TLS ciphers. For more control over the configuration, it is preferable to use a full-featured reverse proxy as described above.
