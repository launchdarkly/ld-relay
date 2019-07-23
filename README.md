LaunchDarkly Relay Proxy
=========================


What is it?
-----------
The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients.

The relay proxy lets a number of servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`.

The relay proxy can be configured to proxy multiple environment streams, even across multiple projects. It can also be used as a local proxy that forwards events  to `events.launchdarkly.com`. This can be useful if you are load balancing LDR instances behind a proxy that times out HTTP connections (e.g. Elastic Load Balancers).


When should it be used?
-----------------------
In most cases, the relay proxy is not required. However, there are some specific scenarios where we recommend deploying the proxy to improve performance and reliability:

1. PHP-- PHP has a shared-nothing architecture that prevents the normal LaunchDarkly streaming API connection from being re-used across requests. While we do have a supported deployment mode for PHP that does not require the relay proxy, we strongly recommend using the proxy in daemon mode (see below) if you are using PHP in a high-throughput setting. This will offload the task of receiving feature flag updates to the relay proxy. We also recommend using the relay to forward events to `events.launchdarkly.com`, and configuring the PHP client to send events to the relay synchronously. This eliminates the curl / fork method that the PHP SDK uses by default to send events back to LaunchDarkly asynchronously.

2. Reducing outbound connections to LaunchDarkly-- at scale (thousands or tens of thousands of servers), the number of outbound persistent connections to LaunchDarkly's streaming API can be problematic for some proxies and firewalls. With the relay proxy in place in proxy mode, your servers can connect directly to hosts within your own datacenter instead of connecting directly to LaunchDarkly's streaming API. On an appropriately spec'd machine, each relay proxy can handle tens of thousands of concurrent connections, so the number of outbound connections to the LaunchDarkly streaming API can be reduced dramatically.

3. Reducing redundant database traffic-- if you are using Redis or another supported database as a shared persistence option for feature flags, and have a large number of servers (thousands or tens of thousands) connected to LaunchDarkly, each server will attempt to update the database when a flag update happens. This pattern is safe but inefficient. By deploying the relay proxy in daemon mode, and setting your LaunchDarkly SDKs to daemon mode, you can delegate flag updates to a small number of relay proxy instances and reduce the number of redundant update calls to the database.


Quick setup
-----------
1. Make sure `go` 1.6+ is installed. Follow the instructions provided in [Go's documentation](https://golang.org/doc/install)

2. Build and install the binary in your $GOPATH:
```
go get -u gopkg.in/launchdarkly/ld-relay.v5/...
```

3. Create LD Relay configuration file. Create a new filed called `ld-relay.conf` containing the text:
```
[main]
    streamUri = "https://stream.launchdarkly.com"
    baseUri = "https://app.launchdarkly.com"
[environment "<NAME-OF-YOUR-ENVIRONMENT>"]
    sdkKey = "<SDK-KEY-FOR-YOUR-ENVIRONMENT>"
```

4. Run the binary by entering the following command in your terminal:
```
$GOPATH/bin/ld-relay --config ./ld-relay.conf
```

5. Validate functionality. This can be done two different ways.
- Update your SDKs configuration. When initializing the SDK, set configuration attribute `streamUri` to the host and port of your relay proxy instance. Note: Your SDK must not be set to polling mode. You must leave streaming enabled to use LD-Relay.
- Manually curl the relay. Run the following command in your terminal:
```
curl -X REPORT localhost:8030/sdk/eval/user -H "Authorization: YOUR_SDK_KEY" -H "Content-Type: application/json" -d '{"key": "a00ceb", "email":"barnie@example.org"}'
```


Command-line arguments
----------------------
argument | default            | description
-------- | ------------------ | -----------
`config` | /etc/ld-relay.conf | configuration file location


Configuration file format
-------------------------
LD Relay uses INI-style configuration files. You can read more about the syntax [here](https://git-scm.com/docs/git-config#_syntax).

There are three primary section types: Main, Events, and Environments. The optional Redis, DynamoDB, and Consul sections are for configuring the desired database, if any. In addition to these, there are three supported sections for configuring exporters for exporting metrics and route traces: Datadog, Stackdriver, and Prometheus.

## [main]
variable name            | type    | default                           | description
------------------------ |:-------:|:---------------------------------:| -----------
`streamUri`              | URI     | `https://stream.launchdarkly.com` | Required. URI from which the relay will stream flag configurations
`baseUri`                | URI     | `https://app.launchdarkly.com`    | Required. URI from which the relay will poll for some information
`exitOnError`            | Boolean | `false`                           | Close the relay if it encounters any error during initialization
`ignoreConnectionErrors` | Boolean | `false`                           | Ignore any initial connectivity issues with LaunchDarkly. Best used when network connectivity is not reliable.
`port`                   | Number  | `8030`                            | Port the LD Relay should listen on
`heartbeatIntervalSecs`  | Number  | `0`                               | If > 0, sends heartbeats to connected clients at this interval

## [events]
variable name       | type    | default                           | description
------------------- |:-------:|:---------------------------------:| ---------------------------------------------------------
`eventsUri`         | URI     | `https://events.launchdarkly.com` | Required to proxy back-end analytic events.
`sendEvents`        | Boolean | `false`                           | When enabled, LD-Relay will send analytic events it receives to LaunchDarkly
`flushIntervalSecs` | Number  | `5`                               | Controls how long the SDK buffers events before sending them back to our server. If your server generates many events per second, we suggest decreasing the flush_interval and / or increasing capacity to meet your needs.
`samplingInterval`  | Number  | `0`                               | Sends every one out of every `samplingInterval` events
`capacity`          | Number  | `1000`                               | Maximum number of events in queue before events are automatically flushed
`inlineUsers`       | Boolean | `false`                           | When enabled, all non-private user attriutes will be sent in events. Otherwise, only the user's key is sent in events

## [redis]
variable name | type   | default | description
------------- |:------:|:-------:| -----------
`host`        | string |         | Hostname of the Redis database
`port`        | Number |         | Port of the Redis database
`url`         | string |         | URL of the Redis database (overrides `host` & `port`)
`localTtl`    | Number | `30000` | Length of time (in milliseconds) that database items can be cached in memory

## [dynamoDB]
variable name | type   | default | description
------------- |:------:|:-------:| -----------
`enabled`     | Boolean |        | Set this to `true` if you are using DynamoDB
`tableName`   | string |         | DynamoDB table name, if you are using the same table for all environments; otherwise, omit this and specify it in each environment section. (Note, credentials and region are controlled by the usual AWS environment variables and/or local AWS configuration files.)
`localTtl`    | Number | `30000` | Length of time (in milliseconds) that database items can be cached in memory

## [consul]
variable name | type   | default | description
------------- |:------:|:-------:| -----------
`host`        | string |         | Hostname of the Consul server
`localTtl`    | Number | `30000` | Length of time (in milliseconds) that database items can be cached in memory

## [environment]
variable name        | type           | description
---------------------|:--------------:| -----------
`sdkKey`             | SDK Key        | SDK key for the environment. Required to proxy back-end SDK functionality
`mobileKey`          | Mobile Key     | Mobile key for the environment. Required to proxy mobile SDK functionality
`envId`              | Client-side ID | Client-side ID for the environment. Required to proxy front-end SDK functionality
`prefix`             | String         | If using a Redis, Consul, or DynamoDB feature store, this string will be added to all database keys to distinguish them from any other environments that are using the database.
`tableName`          | String         | If using DynamoDB, you can specify a different table for each environment. (Or, specify a single table in the `[DynamoDB]` section and use `prefix` to distinguish the environments.)
`allowedOrigin`      | URI            | If provided, adds CORS headers to prevent access from other domains. This variable can be provided multiple times per environment
`insecureSkipVerify` | Boolean        | If true, TLS accepts any certificate presented by the server and any host name in that certificate.

## [datadog]
variable name | type    | description
--------------|:-------:| -----------
`enabled`     | Boolean | If true, enabled exporting to Datadog.
`statsAddr`   | URI     | URI of the DogStatsD agent. If not provided, stats will not be collected. Example: `localhost:8125`
`traceAddr`   | URI     | URI of the Datadog trace agent. If not provided, traces will not be collected. Example: `localhost:8126`
`tag`         | string  | A tag to be applied to all metrics sent to datadog. This variable can be provided multiple times. Must be of the form `key:value`. Example: `instance:blue-jaguar`
`prefix`      | string  | The metrics prefix to be used by Datadog.

## [stackdriver]
variable name | type    | description
--------------|:------: | -----------
`enabled`     | Boolean | If true, enabled exporting metrics and traces to Stackdriver.
`projectID`   | string  | Google cloud project ID.
`prefix`      | string  | The metrics prefix to be used by Stackdriver.

## [prometheus]
variable name | type    | description
--------------|:------: | -----------
`enabled`     | Boolean | If true, enabled exporting traces to Prometheus.
`port`        | Number  | The port that ld-relay will listen to `/metrics` on.
`prefix`      | string  | The metrics prefix to be used by Prometheus.

```
[main]
    streamUri = "https://stream.launchdarkly.com"
    baseUri = "https://app.launchdarkly.com"
    exitOnError = true
    heartbeatIntervalSecs = 15

[environment "Spree Project Production"]
    sdkKey = "SPREE_PROD_API_KEY"

[environment "Spree Project Test"]
    sdkKey = "SPREE_TEST_API_KEY"

[environment "Shopnify Project Production"]
    sdkKey = "SHOPNIFY_PROD_API_KEY"

[environment "Shopnify Project Test"]
    sdkKey = "SHOPNIFY_TEST_API_KEY"
```

Mobile and client-side flag evaluation
----------------
LDR may be optionally configured with a mobile SDK key, and/or an environment ID to enable flag evaluation support for mobile and client-side LaunchDarkly SDKs (Android, iOS, and JavaScript).
```
[environment "Spree Mobile Production"]
    sdkKey = "SPREE_MOBILE_PROD_API_KEY"
    mobileKey = "SPREE_MOBILE_PROD_MOBILE_KEY"

[environment "Spree Webapp Production"]
    sdkKey = "SPREE_WEB_PROD_API_KEY"
    envId = "SPREE_WEB_PROD_ENV_ID"
    allowedOrigin = "http://example.org"
    allowedOrigin = "http://another_example.net"
```

Once a mobile key or environment ID has been configured, you may set the `baseUri` parameter to the host and port of your relay proxy instance in your mobile/client-side SDKs. If you are exposing any of the client-side relay endpoints externally, https should be configured with a TLS termination proxy.


Event forwarding
---------------
LDR can also be used to forward events to `events.launchdarkly.com`. When enabled, the relay will buffer and forward events posted to `/bulk` to `https://events.launchdarkly.com/bulk`. The primary use case for this is PHP environments, where the performance of a local proxy makes it possible to synchronously flush analytics events. To set up event forwarding, add an `events` section to your configuration file:

```
[events]
    eventsUri = "https://events.launchdarkly.com"
    sendEvents = true
    flushIntervalSecs = 5
    samplingInterval = 0
    capacity = 1000
    inlineUsers = false
```

This configuration will buffer events for all environments specified in the configuration file. The events will be flushed every `flushIntervalSecs`. To point our SDKs to the relay for event forwarding, set the `eventsUri` in the SDK to the host and port of your relay instance (or preferably, the host and port of a load balancer fronting your relay instances). Setting `inlineUsers` to `true` preserves full user details in every event (the default is to send them only once per user in an `"index"` event).


Persistent storage
------------------
You can configure LDR nodes to persist feature flag settings in Redis, DynamoDB, or Consul. This provides durability in case of (e.g.) a temporary network partition that prevents LDR from communicating with LaunchDarkly's servers. See [Using a persistent feature store](https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store).

```
# Redis example
[redis]
    host = "localhost"
    port = 6379
    localTtl = 30000

# DynamoDB example
[dynamoDB]
    tableName = "my-feature-flags"
    localTtl = 30000

# Consul example
[consul]
    host = "localhost"
    localTtl = 30000
```

Note that the relay can only use _one_ of these at a time; for instance, enabling both Redis and DynamoDB is an error.

Also note that the LaunchDarkly SDK clients have their own options for configuring persistent storage. If you are using daemon mode (see below) then the clients need to be using the same storage configuration as the relay. If you are not using daemon mode, then the two configurations are completely independent, e.g. you could have a relay using Redis, but a client using Consul or not using persistent storage at all.

Relay proxy mode
----------------
LDR is typically deployed in relay proxy mode. In this mode, several LDR instances are deployed in a high-availability configuration behind a load balancer. LDR nodes do not need to communicate with each other, and there is no master or cluster. This makes it easy to scale LDR horizontally by deploying more nodes behind the load balancer.

![LD Relay with load balancer](relay-lb.png)


Daemon mode
-----------------------------
Optionally, you can configure our SDKs to communicate directly to the persistent store. If you go this route, there is no need to put a load balancer in front of LDR-- we call this daemon mode. This is the preferred way to use LaunchDarkly with PHP (as there's no way to maintain persistent stream connections in PHP).

![LD Relay in daemon mode](relay-daemon.png)

In this example, the persistent store is in Redis. To set up LDR in this mode, provide a Redis host and port, and supply a Redis key prefix for each environment in your configuration file:
```
[redis]
    host = "localhost"
    port = 6379
    localTtl = 30000

[main]
    ...

[environment "Spree Project Production"]
    prefix = "ld:spree:production"
    sdkKey = "SPREE_PROD_API_KEY"

[environment "Spree Project Test"]
    prefix = "ld:spree:test"
    sdkKey = "SPREE_TEST_API_KEY"
```

(The per-environment `prefix` setting can be used the same way with Consul or DynamoDB. Alternately, with DynamoDB you can use a separate `tableName` for each environment.)

You can also configure an in-memory cache for the relay to use so that connections do not always hit redis. To do this, set the `localTtl` parameter in your `redis` configuration section to a number (in milliseconds).

If you're not using a load balancer in front of LDR, you can configure your SDKs to connect to Redis directly by setting `use_ldd` mode to `true` in your SDK, and connecting to Redis with the same host and port in your SDK configuration.


Flag evaluation endpoints
----------------
If you're building an SDK for a language which isn't officially supported by LaunchDarkly, or would like to evaluate feature flags internally without an SDK instance, the relay provides endpoints for evaluating all feature flags for a given user. These endpoints support the GET and REPORT http verbs to pass in users either as base64url encoded path parameters, or in the request body, respectively.

Example cURL requests (default local URI and port):

```
curl -X GET -H "Authorization: YOUR_SDK_KEY" localhost:8030/sdk/eval/users/eyJrZXkiOiAiYTAwY2ViIn0=

curl -X REPORT localhost:8030/sdk/eval/user -H "Authorization: YOUR_SDK_KEY" -H "Content-Type: application/json" -d '{"key": "a00ceb", "email":"barnie@example.org"}'
```


Performance, scaling, and operations
------------
We have done extensive load tests on the relay proxy in AWS / EC2. We have also collected a substantial amount of data based on real-world customer use. Based on our experience, we have several recommendations on how to best deploy, operate, and scale the relay proxy:

* Networking performance is paramount. Memory and CPU are not as critical. The relay proxy should be deployed on boxes with good networking performance. On EC2, we recommend using an instance with [Moderate to High networking performance](http://www.ec2instances.info/) such as `m4.xlarge`. On an `m4.xlarge` instance, a single relay proxy node can easily manage 20,000 concurrent connections.

* If using an Elastic Load Balancer in front of the relay proxy, you may need to [pre-warm](https://aws.amazon.com/articles/1636185810492479) the load balancer whenever connections to the relay proxy are cycled. This might happen when you deploy a large number of new servers that connect to the proxy, or upgrade the relay proxy itself.

Health check
------------
The relay has an additional `status` endpoint which provides the current status of all of the relay's streaming connections. This can obtained by using access `/status` with a get request.

Proxied endpoints
-------------------

The table below describes the endpoints proxied by the LD relay.  In this table:

* *user* is the base64 representation of a user JSON object (e.g. `*"key": "user1"*` => `eyJrZXkiOiAidXNlcjEifQ==`).
* *clientId* is the 32-hexdigit Client-side ID (e.g. `6488674dc2ea1d6673731ba2`)

Endpoint                           | Method        | Auth Header | Description
-----------------                  |:-------------:|:-----------:| -----------
/sdk/eval/*clientId*/users/*user*  | GET           | n/a         | Returns flag evaluation results for a user
/sdk/eval/*clientId*/users         | REPORT        | n/a         | Same as above but request body is user json object
/sdk/evalx/*clientId*/users/*user* | GET           | n/a         | Returns flag evaluation results and additional metadata
/sdk/evalx/*clientId*/users        | REPORT        | n/a         | Same as above but request body is user json object
/sdk/goals/*clientId*              | GET           | n/a         | For JS and other client-side SDKs
/mobile/events                     | POST          | mobile      | For receiving events from mobile SDKs
/mobile/events/bulk                | POST          | mobile      | Same as above
/mobile                            | POST          | mobile      | Same as above
/bulk                              | POST          | sdk         | For receiving events from server-side SDKs
/events/bulk/*clientId*            | POST, OPTIONS | n/a         | For receiving events from JS and other client-side SDKs
/a/*clientId*.gif?d=*events*       | GET, OPTIONS  | n/a         | Same as above
/all                               | GET           | sdk         | SSE stream for all data
/flags                             | GET           | sdk         | Legacy SSE stream for flag data
/ping                              | GET           | sdk         | SSE endpoint that issues "ping" events when there are flag data updates
/ping/*clientId*                   | GET           | n/a         | Same as above but with JS and client-side authorization.
/mping                             | GET           | mobile      | SSE endpoint that issues "ping" events when flags should be re-evaluated
/meval/*user*                      | GET           | mobile      | SSE stream of "ping" and other events for mobile clients
/meval                             | REPORT        | mobile      | Same as above but request body is user json object
/eval/*clientId*/*user*            | GET           | n/a         | SSE stream of "ping" and other events for JS and other client-side SDK listeners
/eval/*clientId*                   | REPORT        | n/a         | Same as above but request body is user json object

Exporting metrics and traces
-------
The relay may be configured to export statistics and route traces to Datadog, Stackdriver, and Prometheus. See the [configuration section](https://github.com/launchdarkly/ld-relay#configuration-file-format) for configuration instructions.

The following metrics are supported:

- `connections`: The number of current proxied streaming connections.
- `newconnections`: The number of streaming connections created.
- `requests`: Number of requests received.

Metrics can be filtered by the following tags:

- `platformCategoryTagKey`: The platform a metric was generated by (e.g. server, browser, or client-side).
- `env`: The name of the LaunchDarkly environment.
- `route`: The request route.
- `method`: The http method used for the request.
- `userAgent`: The user agent used to make the request, typically a LaunchDarkly SDK version. Example: "Node/3.4.0"

**Note:** Traces for stream connections will trace until the connection is closed.

Docker
-------
Using docker is not required, but if you prefer using a docker container we provide a docker entrypoint to make this as easy as possible.

To build the ld-relay container:
```
$ docker build -t ld-relay .
```

In docker, the config file is expected to be found at `/ldr/ld-relay.conf` unless you are using environment variables to configure the relay.

### Docker environment variables
The docker entrypoint uses environment variables to configured the dockerized LD Relay instance.

Note that environment variables are always strings, so the ones listed as "boolean" or "number" are simply describing how the relay will interpret that string. For boolean settings, a value of either `true` or `1` is considered true while any other value is considered false.

environment variable         | type           | default                           | description
---------------------------- |:--------------:|:---------------------------------:| -----------
STREAM_URI                   | URI            | `https://stream.launchdarkly.com` |
BASE_URI                     | URI            | `https://app.launchdarkly.com` |
USE_REDIS                    | Boolean        | `false` | If true, Redis configuration will be added.
REDIS_HOST                   | URI            | `redis` | Sets the hostname of the Redis server. If linked to a redis container that sets `REDIS_PORT` to `tcp://172.17.0.2:6379`, `REDIS_HOST` will use this value as the default.
REDIS_PORT                   | Port           | `6379`  | Sets the port of the Redis server. If linked to a redis container that sets `REDIS_PORT` to `REDIS_PORT=tcp://172.17.0.2:6379`, `REDIS_PORT` will use this value as the default.
REDIS_URL                    | URI            |                                   | URL of the Redis database (overrides `REDIS_HOST` & `REDIS_PORT`)
REDIS_TTL                    | Number         | `30000`                           | Alternate name for CACHE_TTL
USE_DYNAMODB                 | Boolean        | `false`                           | If true, DynamoDB configuration will be added. You must also specify a table name with either `DYNAMODB_TABLE` or `LD_TABLE_NAME_*env_name*` as described below.
DYNAMODB_TABLE               | String         |                                   | DynamoDB table name, if any; if you are using a different table for each environment, leave this blank.
CACHE_TTL                    | Number         | `30000`                           | Sets the local cache TTL in milliseconds if you are using a database.
USE_EVENTS                   | Boolean        | `false`                           | Enables event buffering.
EVENTS_HOST                  | URI            | `https://events.launchdarkly.com` | URI of the LaunchDarkly events endpoint.
EVENTS_FLUSH_INTERVAL        | Number         | `5`                               | Sets how often events are flushed, in seconds.
EVENTS_SAMPLING_INTERVAL     | Number         | `0`                               |
EXIT_ON_ERROR                | Boolean        | `false`                           | If true, the relay will quit at startup time if it cannot establish a connection to LaunchDarkly.
HEARTBEAT_INTERVAL           | Number         | `15`                              |
EVENTS_CAPACITY              | Number         | `10000`                           |
LD_ENV_*env_name*            | SDK Key        |                                   | At least one `LD_ENV_${environment}` variable is recommended. The value should be the SDK key for that specific environment. Multiple environments can be listed.
LD_MOBILE_KEY_*env_name*     | Mobile Key     |                                   | The value should be the Mobile key for that specific environment. Multiple environments can be listed.
LD_CLIENT_SIDE_ID_*env_name* | Client-side ID |                                   | The value should be the environment ID for that specific environment (this is used by the browser JavaScript SDK). Multiple environments can be listed.
LD_PREFIX_*env_name*         | String         |                                   | Configures a database key prefix for that specific environment (with Redis, Consul, or DynamoDB). Multiple environments can be listed.
LD_TABLE_NAME_*env_name*     | String         |                                   | Configures a database table name for that specific environment (with DynamoDB only). Multiple environments can be listed.
USE_DATADOG                  | Boolean        | `false`                           | Enables metric exports to DataDog.
DATADOG_STATS_ADDR           | String         | `localhost:8125`                  | URI of the DataDog stats agent.
DATADOG_TRACE_ADDR           | String         | `localhost:8126`                  | URI of the DataDog trace agent.
DATADOG_PREFIX               | String         |                                   | Configure a prefix for DataDog metric names.
DATADOG_TAG_*tag_name*       | String         |                                   | Configure tags to be associated with DataDog metrics.
USE_STACKDRIVER              | Boolean        | `false`                           | Enables metric exports to Stackdriver.
STACKDRIVER_PROJECT_ID       | String         |                                   | Stackdriver project id. Required to successfully export metrics to Stackdriver.
STACKDRIVER_PREFIX           | String         |                                   | Configure a prefix for Stackdriver metric names.
USE_PROMETHEUS               | Boolean        | `false`                           | Enables metric exports to Prometheus.
PROMETHEUS_PREFIX            | String         |                                   | Configure a prefix for Prometheus metric names.
PROMETHEUS_PORT              | Number         | 8031                              | The port that ld-relay will listen to `/metrics` on.

### Docker examples
To run a single environment, without Redis:
```
$ docker run --name ld-relay -e LD_ENV_test="sdk-test-sdkKey" ld-relay
```

To run multiple environments, without Redis:
```
$ docker run --name ld-relay -e LD_ENV_test="sdk-test-sdkKey" -e LD_ENV_prod="sdk-prod-sdkKey" ld-relay
```

To run a single environment, with Redis:
```
$ docker run --name redis redis:alpine
$ docker run --name ld-relay --link redis:redis -e USE_REDIS=1 -e LD_ENV_test="sdk-test-sdkKey" ld-relay
```

To run multiple environment, with Redis:
```
$ docker run --name redis redis:alpine
$ docker run --name ld-relay --link redis:redis -e USE_REDIS=1 -e LD_ENV_test="sdk-test-sdkKey" -e LD_PREFIX_test="ld:default:test" -e LD_ENV_prod="sdk-prod-sdkKey" -e LD_PREFIX_prod="ld:default:prod" ld-relay
```


Windows
-------
To register ld-relay as a service, run a command prompt as Administrator
```
$ sc create ld-relay DisplayName="LaunchDarkly Relay Proxy" start="auto" binPath="C:\path\to\ld-relay.exe -config C:\path\to\ld-relay.conf"
```


Integrating LD Relay into your own application
----------------------------------------------

You can also use relay to handle endpoints in your own application if you don't want to use the default `ld-relay` application.  Below is an
example using [Gorilla](https://github.com/gorilla/mux) of how you might instantiate a relay inside your web server beneath a path called "/relay":

```go
router := mux.NewRouter()
configFileName := "path/to/my-config-file"
cfg := relay.DefaultConfig
if err := relay.LoadConfigFile(&cfg, configFileName); err != nil {
    log.Fatalf("Error loading config file: %s", err)
}
r, err := relay.NewRelay(cfg, relay.DefaultClientFactory)
if err != nil {
    log.Fatalf("Error creating relay: %s", err)
}
router.PathPrefix("/relay").Handler(r)
```

The above example uses a configuration file. You can also pass in a `relay.Config` struct that you have filled in directly:

```go
cfg := relay.DefaultConfig
cfg.Main.Port = 5000
cfg.Environment = map[string]*relay.EnvConfig{
    "Spree Project Production": &relay.EnvConfig{
        SdkKey: "SPREE_PROD_API_KEY",
    }
}
r, err := relay.NewRelay(cfg, relay.DefaultClientFactory)
```

Or, you can parse the configuration from a string that is in the same format as the configuration file, using the same `gcfg` package that ld-relay uses:

```go
import "github.com/launchdarkly/gcfg"

configString := `[main]\nport = 5000\n[environment "Spree Project Production"]\nsdkKey = "SPREE_PROD_API_KEY"`

cfg := relay.DefaultConfig
if err := gcfg.ReadStringInto(&cfg, configString); err != nil {
    log.Fatalf("Error loading config file: %s", err)
}
r, err := relay.NewRelay(cfg, relay.DefaultClientFactory)
```

Testing
-------

After installing a compatible version of Go, run `make test` to build and run unit tests. To run integration runs, run `make integration-test`. To run the linter, run `make lint`.
