LaunchDarkly Relay Proxy
=========================

What is it?
-----------

The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients. 

The relay proxy lets a number of servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`.

The relay proxy can be configured to proxy multiple environment streams, even across multiple projects.

Quick setup
-----------

1. Copy `ld-relay.conf` to `/etc/ld-relay.conf` (or elsewhere), and edit to specify your port and LaunchDarkly API keys for each environment you wish to proxy.

2. If building from source, have `go` 1.6+ and `godep` installed, and run `godep go build`.

3. Run `ld-relay --config <configDir>/ld-relay.conf`. If the `--config` parameter is not specified, `ld-relay` defaults to `/etc/ld-relay.conf`.

4. Set `stream` to `true` in your application's LaunchDarkly SDK configuration. Set the `streamUri` parameter to the host and port of your relay proxy instance.

Configuration file format 
-------------------------

You can configure LDR to proxy as many environments as you want, even across different projects. You can also configure LDR to send periodic heartbeats to connected clients. This can be useful if you are load balancing LDR instances behind a proxy that times out HTTP connections (e.g. Elastic Load Balancers).

Here's an example configuration file that synchronizes four environments across two different projects (called Spree and Shopnify), and sends heartbeats every 15 seconds:

        [main]
        streamUri = "https://stream.launchdarkly.com"
        baseUri = "https://app.launchdarkly.com"
        exitOnError = false
        ignoreConnectionErrors = true
        heartbeatIntervalSecs = 15

        [environment "Spree Project Production"]
        apiKey = "SPREE_PROD_API_KEY"

        [environment "Spree Project Test"]
        apiKey = "SPREE_TEST_API_KEY"

        [environment "Shopnify Project Production"]
        apiKey = "SHOPNIFY_PROD_API_KEY"

        [environment "Shopnify Project Test"]
        apiKey = "SHOPNIFY_TEST_API_KEY"

Redis storage and daemon mode
-----------------------------

You can configure LDR to persist feature flag settings in Redis. This provides durability in case of (e.g.) a temporary network partition that prevents LDR from 
communicating with LaunchDarkly's servers.

Optionally, you can configure our SDKs to communicate directly to the Redis store. If you go this route, there is no need to put a load balancer in front of LDR-- we call this daemon mode. 

To set up LDR in this mode, provide a redis host and port, and supply a Redis key prefix for each environment in your configuration file:

        [redis]
        host = "localhost"
        port = 6379
        localTtl = 30000

        [main]
        ...

        [environment "Spree Project Production"]
        prefix = "ld:spree:production"
        apiKey = "SPREE_PROD_API_KEY"

        [environment "Spree Project Test"]
        prefix = "ld:spree:test"
        apiKey = "SPREE_TEST_API_KEY"

You can also configure an in-memory cache for the relay to use so that connections do not always hit redis. To do this, set the `localTtl` parameter in your `redis` configuration section to a number (in milliseconds). 

If you're not using a load balancer in front of LDR, you can configure your SDKs to connect to Redis directly by setting `use_ldd` mode to `true` in your SDK, and connecting to Redis with the same host and port in your SDK configuration.


