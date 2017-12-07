LaunchDarkly Relay Proxy
=========================

What is it?
-----------

The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients. 

The relay proxy lets a number of servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`. 

The relay proxy can be configured to proxy multiple environment streams, even across multiple projects. It can also be used as a local proxy that forwards events  to `events.launchdarkly.com`.

When should it be used?
-----------------------

In most cases, the relay proxy is unnecessary. However, there are some specific scenarios where we recommend deploying the proxy to improve performance and reliability:

1. PHP-- PHP has a shared-nothing architecture that prevents the normal LaunchDarkly streaming API connection from being re-used across requests. While we do have a supported deployment mode for PHP that does not require the relay proxy, we strongly recommend using the proxy in daemon mode (see below) if you are using PHP in a high-throughput setting. This will offload the task of receiving feature flag updates to the relay proxy. We also recommend using the relay to forward events to `events.launchdarkly.com`, and configuring the PHP client to send events to the relay synchronously. This eliminates the curl / fork method that the PHP SDK uses by default to send events back to LaunchDarkly asynchronously.

2. Reducing outbound connections to LaunchDarkly-- at scale (thousands or tens of thousands of servers), the number of outbound persistent connections to LaunchDarkly's streaming API can be problematic for some proxies and firewalls. With the relay proxy in place in proxy mode, your servers can connect directly to hosts within your own datacenter instead of connecting directly to LaunchDarkly's streaming API. On an appropriately spec'd machine, each relay proxy can handle tens of thousands of concurrent connections, so the number of outbound connections to the LaunchDarkly streaming API can be reduced dramatically.

3. Reducing redundant update traffic to Redis-- if you are using Redis as a shared persistence option for feature flags, and have a large number of servers (thousands or tens of thousands) connected to LaunchDarkly, each server will attempt to update Redis when a flag update happens. This pattern is safe but inefficient. By deploying the relay proxy in daemon mode, and setting your LaunchDarkly SDKs to daemon mode, you can delegate flag updates to a small number of relay proxy instances and reduce the number of redundant update calls to Redis.

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

Your configuration file must include at least one environment.

Relay proxy mode
----------------

LDR is typically deployed in relay proxy mode. In this mode, several LDR instances are deployed in a high-availability configuration behind a load balancer. LDR nodes do not need to communicate with each other, and there is no master or cluster. This makes it easy to scale LDR horizontally by deploying more nodes behind the load balancer.


![LD Relay with load balancer](relay-lb.png)


Redis storage
-------------

You can configure LDR nodes to persist feature flag settings in Redis. This provides durability in case of (e.g.) a temporary network partition that prevents LDR from communicating with LaunchDarkly's servers.


Daemon mode
-----------------------------

Optionally, you can configure our SDKs to communicate directly to the Redis store. If you go this route, there is no need to put a load balancer in front of LDR-- we call this daemon mode. This is the preferred way to use LaunchDarkly with PHP (as there's no way to maintain persistent stream connections in PHP).

![LD Relay in daemon mode](relay-daemon.png)

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


Event forwarding
---------------

LDR can also be used to forward events to `events.launchdarkly.com`. When enabled, the relay will buffer and forward events posted to `/bulk` to `https://events.launchdarkly.com/bulk`. The primary use case for this is PHP environments, where the performance of a local proxy makes it possible to synchronously flush analytics events. To set up event forwarding, add an `events` section to your configuration file:

        [events]
        eventsUri = "https://events.launchdarkly.com"
        sendEvents = true
        flushIntervalSecs = 5
        samplingInterval = 0
        capacity = 10000

This configuration will buffer events for all environments specified in the configuration file. The events will be flushed every `flushIntervalSecs`. To point our SDKs to the relay for event forwarding, set the `eventsUri` in the SDK to the host and port of your relay instance (or preferably, the host and port of a load balancer fronting your relay instances).

Performance, scaling, and operations
------------

We have done extensive load tests on the relay proxy in AWS / EC2. We have also collected a substantial amount of data based on real-world customer use. Based on our experience, we have several recommendations on how to best deploy, operate, and scale the relay proxy:

* Networking performance is paramount. Memory and CPU are not as critical. The relay proxy should be deployed on boxes with good networking performance. On EC2, we recommend using an instance with [Moderate to High networking performance](http://www.ec2instances.info/) such as `m4.xlarge`. On an `m4.xlarge` instance, a single relay proxy node can easily manage 20,000 concurrent connections.

* If using an Elastic Load Balancer in front of the relay proxy, you may need to [pre-warm](https://aws.amazon.com/articles/1636185810492479) the load balancer whenever connections to the relay proxy are cycled. This might happen when you deploy a large number of new servers that connect to the proxy, or upgrade the relay proxy itself.

HTTPS proxy
------------
Go's standard HTTP library provides a built-in HTTPS proxy. If the HTTPS_PROXY environmental variable is present then the SDK will proxy all network requests through the URL provided.

Docker
-------

To build the ld-relay container:

        $ docker build -t ld-relay-build -f Dockerfile-build . # create the build container image
        $ docker run -v $(pwd):/build -t -i -e CGO_ENABLED=0 -e GOOS=linux ld-relay-build godep go build -a -installsuffix cgo -o ldr # create the ldr binary
        $ docker build -t ld-relay . # build the ld-relay container image
        $ docker rmi ld-relay-build # remove the build container image that is no longer needed

To run a single environment, without Redis:

        $ docker run --name ld-relay -e LD_ENV_test="sdk-test-apiKey" ld-relay

To run multiple environments, without Redis:

        $ docker run --name ld-relay -e LD_ENV_test="sdk-test-apiKey" -e LD_ENV_prod="sdk-prod-apiKey" ld-relay

To run a single environment, with Redis:

        $ docker run --name redis redis:alpine
        $ docker run --name ld-relay --link redis:redis -e USE_REDIS=1 -e LD_ENV_test="sdk-test-apiKey" ld-relay

To run multiple environment, with Redis:

        $ docker run --name redis redis:alpine
        $ docker run --name ld-relay --link redis:redis -e USE_REDIS=1 -e LD_ENV_test="sdk-test-apiKey" -e LD_PREFIX_test="ld:default:test" -e LD_ENV_prod="sdk-prod-apiKey" -e LD_PREFIX_prod="ld:default:prod" ld-relay


Docker Environment Variables
-------

`LD_ENV_${environment}`: At least one `LD_ENV_${environment}` variable is recommended.  The value should be the api key for that specific environment.  Multiple environments can be listed

`LD_PREFIX_${environment}`: This variable is optional.  Configures a Redis prefix for that specific environment.  Multiple environments can be listed

`USE_REDIS`: This variable is optional.  If set to 1, Redis configuration will be added

`REDIS_HOST`: This variable is optional.  Sets the hostname of the Redis server.  If linked to a redis container that sets `REDIS_PORT` to `tcp://172.17.0.2:6379`, `REDIS_HOST` will use this value as the default.  If not, the default value is `redis`

`REDIS_PORT`: This variable is optional.  Sets the port of the Redis server.  If linked to a redis container that sets `REDIS_PORT` to `REDIS_PORT=tcp://172.17.0.2:6379`, `REDIS_PORT` will use this value as the default.  If not, the defualt value is `6379`

`REDIS_TTL`: This variable is optional.  Sets the TTL in milliseconds, defaults to `30000`

`USE_EVENTS`: This variable is optional.  If set to 1, enables event buffering

`EVENTS_HOST`: This variable is optional.  URI of the LaunchDarkly events endpoint, defaults to `https://events.launchdarkly.com`

`EVENTS_SEND`: This variable is optional.  Defaults to `true`

`EVENTS_FLUSH_INTERVAL`: This variable is optional.  Sets how often events are flushed, defaults to `5` (seconds)

`EVENTS_SAMPLING_INTERVAL`: This variable is optional.  Defaults to `10000`

