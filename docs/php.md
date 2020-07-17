# LaunchDarkly Relay Proxy - Using with PHP

[(back to README)](../README.md)

The [PHP SDK](https://github.com/launchdarkly/php-server-sdk) communicates differently with LaunchDarkly than the other SDKs because it does not support long-lived streaming connections. It must either poll for flags on demand via HTTP, or get them from Redis or another database.

A database is more efficient, and is the preferred approach. **See: [Daemon Mode](./daemon-mode.md)**

If you are not using a database, the Relay Proxy can handle HTTP requests from PHP ([proxy mode](./proxy-mode.md)). However, it is highly recommended that if you do this, you use the `ttlMinutes` parameter in the [environment configuration](./configuration.md#file-section-environment-name) to enable HTTP caching. This is equivalent to the [TTL setting for the environment on your LaunchDarkly dashboard](https://docs.launchdarkly.com/home/managing-flags/environments#ttl-settings), but must be set here separately because the Relay Proxy does not have access to those dashboard properties. This will cause HTTP responses from the PHP endpoints to have a `Cache-Control: max-age` so that the PHP SDK will not make additional HTTP requests for the same flag more often than that interval.

Note that this may result in different PHP application instances receiving flag updates at slightly different times as their HTTP caches will not be exactly in sync. It does not affect any SDKs other than PHP.
