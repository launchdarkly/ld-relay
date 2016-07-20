LaunchDarkly Relay Proxy
=========================

What is it?
-----------

The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients. 

The relay proxy lets a servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`.

The relay proxy can be configured to proxy multiple environment streams, even across multiple projects.

Quick setup
-----------

1. Copy `ld-relay.conf` to `/etc/ld-relay.conf`, and edit to specify your port and LaunchDarkly API keys for each environment you wish to proxy.

2. If building from source, have `go` 1.4+ and `godep` installed, and run `godep go build`.

3. Run `ld-relay`.

4. Set `stream` to `true` in your application's LaunchDarkly SDK configuration. Set the `streamUri` parameter to the host and port of your relay proxy instance.

Configuration file format 
-------------------------

You can configure LDR to proxy as many environments as you want, even across different projects. You can also configure LDR to send periodic heartbeats to connected clients. This can be useful if you are load balancing LDR instances behind a proxy that times out HTTP connections (e.g. Elastic Load Balancers).

Here's an example configuration file that synchronizes four environments across two different projects (called Spree and Shopnify), and sends heartbeats every 15 seconds:

        [main]
        streamUri = "https://stream.launchdarkly.com"
        baseUri = "https://app.launchdarkly.com"
        exitOnError = false
        heartbeatIntervalSecs = 15

        [environment "Spree Project Production"]
        apiKey = "SPREE_PROD_API_KEY"

        [environment "Spree Project Test"]
        apiKey = "SPREE_TEST_API_KEY"

        [environment "Shopnify Project Production"]
        apiKey = "SHOPNIFY_PROD_API_KEY"

        [environment "Shopnify Project Test"]
        apiKey = "SHOPNIFY_TEST_API_KEY"
