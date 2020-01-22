# LaunchDarkly Relay Proxy

## What is it?

The LaunchDarkly Relay Proxy establishes a connection to the LaunchDarkly streaming API, then proxies that stream connection to multiple clients.

The Relay Proxy lets a number of servers connect to a local stream instead of making a large number of outbound connections to `stream.launchdarkly.com`.

The Relay Proxy can be configured to proxy multiple environment streams, even across multiple projects. It can also be used as a local proxy that forwards events  to `events.launchdarkly.com`. This can be useful if you are load balancing Relay Proxy instances behind a proxy that times out HTTP connections (e.g. Elastic Load Balancers).


## When should it be used?

In most cases, the Relay Proxy is not required. However, there are some specific scenarios where we recommend deploying it to improve performance and reliability:

1. **PHP.** PHP has a shared-nothing architecture that prevents the normal LaunchDarkly streaming API connection from being re-used across requests. While we do have a supported deployment mode for PHP that does not require the Relay Proxy, we strongly recommend using the Relay Proxy in daemon mode (see below) if you are using PHP in a high-throughput setting. This will offload the task of receiving feature flag updates to the Relay Proxy. We also recommend using the Relay Proxy to forward events to `events.launchdarkly.com`, and configuring the PHP client to send events to the Relay Proxy synchronously. This eliminates the curl/fork method that the PHP SDK uses by default to send events back to LaunchDarkly asynchronously.

2. **Reducing outbound connections to LaunchDarkly.** At scale (thousands or tens of thousands of servers), the number of outbound persistent connections to LaunchDarkly's streaming API can be problematic for some proxies and firewalls. With the Relay Proxy in place in proxy mode, your servers can connect directly to hosts within your own datacenter instead of connecting directly to LaunchDarkly's streaming API. On an appropriately spec'd machine, each Relay Proxy can handle tens of thousands of concurrent connections, so the number of outbound connections to the LaunchDarkly streaming API can be reduced dramatically.

3. **Reducing redundant database traffic.** If you are using Redis or another supported database as a shared persistence option for feature flags, and have a large number of servers (thousands or tens of thousands) connected to LaunchDarkly, each server will attempt to update the database when a flag update happens. This pattern is safe but inefficient. By deploying the Relay Proxy in daemon mode, and setting your LaunchDarkly SDKs to daemon mode, you can delegate flag updates to a small number of Relay Proxy instances and reduce the number of redundant update calls to the database.


## Getting started

Refer to [our documentation](https://docs.launchdarkly.com/docs/using-the-relay-proxy#section-starting-the-relay-proxy) for instructions on getting started with using the Relay Proxy.


## Command-line arguments

Argument               | Default              | Description
---------------------- | -------------------- | -----------
`--config`             | `/etc/ld-relay.conf` | configuration file location
`--allow-missing-file` |                      | if specified, a `--config` option for a nonexistent file will be ignored
`--from-env`           |                      | if specified, configuration will be read from environment variables

See the next section for how these options may be used separately or together.


## Specifying a configuration

Configuration options may be passed in a file, or in environment variables, or both. The command-line arguments for these work as follows:

* If you pass no arguments at all, it will attempt to load `/etc/ld-relay.conf`.
* If you pass `--config FILEPATH`, it will load that file. The file must exist.
* If you pass `--config FILEPATH --allow-missing-file`, it will try to load the file only if the file exists.
* If you pass `--from-env`, it will read configuration options from environment variables.
* If you pass both `--config` and `--from-env`, it will both load the specified file and use the environment variables. The environment variables will override any equivalent options from the file.

An example of why you might use both configuration modes together is if you want to deploy a `base.conf` file that contains all of the global configuration for your relay instance, but for security reasons you do not want your SDK key to appear in that file. Assuming that the name you gave your LaunchDarkly environment in the file is "production", your command line might look like this:

```shell
LD_ENV_production={your_SDK_key} ./ld-relay --config base.conf --from-env
```

Or, you might wish to create a package containing the `ld-relay` binary and a file with some basic options, which you will be reusing in different contexts with completely different sets of environments. You could completely omit the environment configuration from the file, and pass it all in variables:

```shell
LD_ENV_firstenv={SDK key for firstenv} LD_PREFIX_firstenv={Redis prefix for firstenv} \
  LD_ENV_secondenv={SDK key for secondenv} LD_PREFIX_secondenv={Redis prefix for secondenv} \
  ./ld-relay --config base.conf --from-env
```


## Configuration file format and environment variables

The configuration file format is an INI-like one, based on [Git configuration format](https://git-scm.com/docs/git-config#_syntax) (as implemented by a [fork](https://github.com/launchdarkly/gcfg) of the [gcfg](https://github.com/go-gcfg/gcfg) package).

Every configuration file option has an equivalent environment variable. You may use either method: see ["Specifying a configuration"](#specifying-a-configuration). Note that for boolean settings, a value of either `true` or `1` is considered true while any other value (or an empty value) is considered false.

### File section: `[Main]`

Property in file         | Environment var      | Type    | Default | Description
------------------------ | -------------------- | :-----: | :------ | -----------
`streamUri`              | `STREAM_URI`         | URI     | _(1)_   | URI for the LaunchDarkly streaming service.
`baseUri`                | `BASE_URI`           | URI     | _(1)_   | URI for the LaunchDarkly polling service.
`exitOnError`            | `EXIT_ON_ERROR`      | Boolean | `false` | Close the Relay Proxy if it encounters any error during initialization.
`exitAlways`             | `EXIT_ALWAYS`        | Boolean | `false`  | Close the Relay Proxy immediately after initializing all environments (do not start an HTTP server). _(2)_
`ignoreConnectionErrors` | `IGNORE_CONNECTION_ERRORS` | Boolean | `false` | Ignore any initial connectivity issues with LaunchDarkly. Best used when network connectivity is not reliable.
`port`                   | `PORT`               | Number  | `8030`  | Port the Relay Proxy should listen on.
`heartbeatIntervalSecs`  | `HEARTBEAT_INTERVAL` | Number  | `180`   | Interval (in seconds) for heartbeat messages to prevent read timeouts on streaming connections.
`tlsEnabled`             | `TLS_ENABLED`        | Boolean | `false` | Enable TLS on the Relay Proxy.
`tlsCert`                | `TLS_CERT`           | String  |         | Required if `tlsEnabled` is true. Path to TLS certificate file.
`tlsKey`                 | `TLS_KEY`            | String  |         | Required if `tlsEnabled` is true. Path to TLS private key file.
`logLevel`               | `LOG_LEVEL`          | String  | `info`  | Should be `debug`, `info`, `warn`, `error`, or `none`; see [Logging](#logging)

_(1)_ The default values for `streamUri` and `baseUri` are `https://app.launchdarkly.com` and `https://stream.launchdarkly.com`. You should never need to change these URIs unless a) you are using a special instance of the LaunchDarkly service, in which case support will tell you how to set them, or b) you are accessing LaunchDarkly via a reverse proxy or some other mechanism that rewrites URLs.

_(2)_ The `exitAlways` mode is intended for use cases where you do not want to maintain a long-running Relay Proxy instance, but only execute it at specific times to get flags; this is only useful if you have enabled Redis or another database, so that it will store the flags there.

### File section: `[Events]`

Property in file    | Environment var            | Type    | Default | Description
------------------- | -------------------------- | :-----: | :------ | -----------
`sendEvents`        | `USE_EVENTS`               | Boolean | `false` | When enabled, LD-Relay will send analytic events it receives to LaunchDarkly.
`eventsUri`         | `EVENTS_HOST`              | URI     | _(2)_   | URI for the LaunchDarkly events service
`flushIntervalSecs` | `EVENTS_FLUSH_INTERVAL`    | Number  | `5`     | Controls how long the SDK buffers events before sending them back to our server. If your server generates many events per second, we suggest decreasing the flush interval and/or increasing capacity to meet your needs.
`samplingInterval`  | `EVENTS_SAMPLING_INTERVAL` | Number  | `0`     | Sends one out of this many events as a random sampling.
`capacity`          | `EVENTS_CAPACITY`          | Number  | `1000`  | Maximum number of events to accumulate for each flush interval.
`inlineUsers`       | `EVENTS_INLINE_USERS`      | Boolean | `false` | When enabled, individual events (if full event tracking is enabled for the feature flag) will contain all non-private user attributes.

_(2)_ See note _(1)_ above. The default value for `eventsUri` is `https://events.launchdarkly.com`.

### File section: `[Redis]`

Property in file | Environment var  | Type    | Default | Description
---------------- | ---------------- | :-----: | :------ | -----------
n/a              | `USE_REDIS`      | Boolean | `false`     | If you are using environment variables, set this to enable Redis.
`host`           | `REDIS_HOST`     | String  | `localhost` | Hostname of the Redis database. Redis is enabled if this or `url` is set.
`port`           | `REDIS_PORT`     | Number  | `6379`      | Port of the Redis database. Note that if you are using environment variables, setting `REDIS_PORT` to a string like `tcp://host:port` sets both the host and the port; this is used in Docker.
`url`            | `REDIS_URL`      | String  |             | URL of the Redis database (overrides `host` & `port`).
`tls`            | `REDIS_TLS`      | Boolean | `false`     | If `true`, will use a secure connection to Redis (not all Redis servers support this). If you specified a `redis://` URL, setting `tls` to `true` will change it to `rediss://`.
`password`       | `REDIS_PASSWORD` | String  |             | Optional password if Redis require authentication.
`localTtl`       | `CACHE_TTL`      | Number  | `30000`     | Length of time (in milliseconds) that database items can be cached in memory.

Note that the TLS and password options can also be specified as part of the URL: `rediss://` instead of `redis://` enables TLS, and `redis://:password@host` instead of `redis://host` sets a password. You may want to use the separate options instead if, for instance, you want your configuration file to contain the basic Redis configuration, but for security reasons you would rather set the password in an environment variable (`REDIS_PASSWORD`).

### File section: `[DynamoDB]`

Property in file    | Environment var    | Type    | Default | Description
------------------- | ------------------ | :-----: | :------ | -----------
`enabled`           | `USE_DYNAMODB`     | Boolean | `false` | Enables DynamoDB.
`tableName`         | `DYNAMODB_TABLE`   | String  |         | The DynamoDB table name, if you are using the same table for all environments. Otherwise, omit this and specify it in each environment section. (Note, credentials and region are controlled by the usual AWS environment variables and/or local AWS configuration files.)
`url`               | `DYNAMODB_URL`     | String  |         | The service endpoint if you are using a local DynamoDB instance instead of the regular service.
`localTtl`          | `CACHE_TTL`        | Number  | `30000`     | Length of time (in milliseconds) that database items can be cached in memory.

The AWS credentials and region for DynamoDB are not part of the Relay configuration; they should be set using either the standard AWS environment variables or a local AWS configuration file, as documented for [the AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-envvars.html).

### File section: `[Consul]`

Property in file | Environment var | Type    | Default     | Description
---------------- | --------------- | :-----: | :---------- | -----------
n/a              | `USE_CONSUL`    | Boolean | `false`     | If you are using environment variables, set this to enable Consul.
`host`           | `CONSUL_HOST`   | String  | `localhost` | Hostname of the Consul server. Consul is enabled if this is set.
`localTtl`       | `CACHE_TTL`     | Number  | `30000`     | Length of time (in milliseconds) that database items can be cached in memory.

### File section: `[Environment "NAME"]`

The Relay Proxy allows you to proxy any number of LaunchDarkly environments; there must be at least one. In a configuration file, each of these is a separate section in the format `[Environment "MyEnvName"]`, where `MyEnvName` is a unique identifier for the environment (this does not have to match the environment name on your LaunchDarkly dashboard, but it is recommended to). If you are using environment variables, you will add the `MyEnvName` identifier to the variable name prefix for each property. See examples below.

Property in file | Environment var               | Type   | Description
---------------- | ----------------------------- | :----: | -----------
`sdkKey`         | `LD_ENV_MyEnvName`            | String | Server-side SDK key for the environment. Required.
`mobileKey`      | `LD_MOBILE_KEY_MyEnvName`     | String | Mobile key for the environment. Required if you are proxying mobile SDK functionality.
`envId`          | `LD_CLIENT_SIDE_ID_MyEnvName` | String | Client-side ID for the environment. Required if you are proxying client-side JavaScript-based SDK functionality.
`prefix`         | `LD_PREFIX_MyEnvName`         | String | If using a Redis, Consul, or DynamoDB feature store, this string will be added to all database keys to distinguish them from any other environments that are using the database.
`tableName`      | `LD_TABLE_NAME_MyEnvName`     | String | If using DynamoDB, you can specify a different table for each environment. (Or, specify a single table in the `[DynamoDB]` section and use `prefix` to distinguish the environments.)
`allowedOrigin`  | `LD_ALLOWED_ORIGIN_MyEnvName` | URI    | If provided, adds CORS headers to prevent access from other domains. This variable can be provided multiple times per environment (if using the `LD_ALLOWED_ORIGIN_MyEnvName` variable, specify a comma-delimited list).
`logLevel`       | `LD_LOG_LEVEL_MyEnvName`      | String | Should be `debug`, `info`, `warn`, `error`, or `none`; see [Logging](#logging)
`ttlMinutes`     | `LD_TTL_MINUTES_MyEnvName`    | Number | HTTP caching TTL for the PHP polling endpoints (see [Using with PHP](#using-with-php))

In the following examples, there are two environments, each of which has a server-side SDK key and a mobile key. Debug-level logging is enabled for the second one.

```
# Configuration file example

[Environment "Spree Project Production"]
    sdkKey = "SPREE_PROD_SDK_KEY"
    mobileKey = "SPREE_PROD_MOBILE_KEY"

[Environment "Spree Project Test"]
    sdkKey = "SPREE_TEST_SDK_KEY"
    mobileKey = "SPREE_TEST_MOVILE_KEY"
    logLevel = "debug"
```

```
# Environment variables example

LD_ENV_Spree_Project_Production=SPREE_PROD_SDK_KEY
LD_MOBILE_KEY_Spree_Project_Production=SPREE_PROD_MOBILE_KEY
LD_ENV_Spree_Project_Test=SPREE_TEST_SDK_KEY
LD_MOBILE_KEY_Spree_Project_Test=SPREE_TEST_MOBILE_KEY
```

### File section: `[Datadog]`

Property in file | Environment var       | Type    | Default | Description
---------------- | --------------------- | :-----: | :------ | -----------
`enabled`        | `USE_DATADOG`         | Boolean | false   | If true, enables exporting to Datadog.
`statsAddr`      | `DATADOG_STATS_ADDR`  | URI     |         | URI of the DogStatsD agent. If not provided, stats will not be collected. Example: `localhost:8125`
`traceAddr`      | `DATADOG_TRACE_ADDR`  | URI     |         | URI of the Datadog trace agent. If not provided, traces will not be collected. Example: `localhost:8126`
`tag`            | `DATADOG_TAG_TagName` | String  |         | A tag to be applied to all metrics sent to datadog. This variable can be provided multiple times (see below).
`prefix`         | `DATADOG_PREFIX`      | String  |         | The metrics prefix to be used by Datadog.

There may be any number of DataDog tags. Use the following format:

```
# Configuration file example

[Datadog]
    enabled = true
    tag = firstTagName:firstTagValue
    tag = secondTagName:secondTagValue
```

```
# Environment variables example

USE_DATADOG=1
DATADOG_TAG_firstTagName=firstTagValue
DATADOG_TAG_secondTagName=secondTagValue
```

### File section: `[Stackdriver]`

Property in file | Environment var          | Type    | Default | Description
---------------- | ------------------------ | :-----: | :------ | -----------
`enabled`        | `USE_STACKDRIVER`        | Boolean | `false` | If true, enables exporting metrics and traces to Stackdriver.
`projectID`      | `STACKDRIVER_PROJECT_ID` | String  |         | Google cloud project ID.
`prefix`         | `STACKDRIVER_PREFIX`     | String  |         | The metrics prefix to be used by Stackdriver.

### File section: `[Prometheus]`

Property in file | Environment var     | Type    | Default | Description
---------------- | ------------------- | :-----: | :------ | -----------
`enabled`        | `USE_PROMETHEUS`    | Boolean | `false` | If true, enables exporting traces to Prometheus.
`port`           | `PROMETHEUS_PORT`   | Number  | `8031`  | The port that the Relay Proxy will provide the `/metrics` endpoint on.
`prefix`         | `PROMETHEUS_PREFIX` | String  |         | The metrics prefix to be used by Prometheus.

### File section: `[Proxy]`

Property in file | Environment var       | Type    | Default | Description
---------------- | --------------------- | :-----: | :------ | -----------
`url`            | `PROXY_URL`           | String  |         | All Relay Proxy network traffic will be sent through this HTTP proxy if specified.
`user`           | `PROXY_AUTH_USER`     | String  |         | Username for proxy authentication, if applicable.
`password`       | `PROXY_AUTH_PASSWORD` | String  |         | Password for proxy authentication, if applicable.
`domain`         | `PROXY_AUTH_DOMAIN`   | String  |         | Domain name for proxy authentication, if applicable.
`caCertFiles`    | `PROXY_CA_CERTS`      | String  |         | Comma-delimited list of file paths to additional CA certificates that should be trusted (in PEM format).
`ntlmAuth`       | `PROXY_AUTH_NTLM`     | Boolean | `false` | Enables NTLM proxy authentication (requires user, password, and domain).


## Mobile and client-side flag evaluation

The Relay Proxy may be optionally configured with a mobile SDK key, and/or an environment ID to enable flag evaluation support for mobile and client-side LaunchDarkly SDKs (Android, iOS, and JavaScript). In these examples, one environment allows only mobile and another allows only client-side JavaScript, but you could also have an environment that uses both.

```
# Configuration file example

[Environment "Spree Mobile Production"]
    sdkKey = "SPREE_MOBILE_PROD_SDK_KEY"
    mobileKey = "SPREE_MOBILE_PROD_MOBILE_KEY"

[Environment "Spree Webapp Production"]
    sdkKey = "SPREE_WEB_PROD_SDK_KEY"
    envId = "SPREE_WEB_PROD_ENV_ID"
    allowedOrigin = "http://example.org"
    allowedOrigin = "http://another_example.net"
```

```
# Environment variables example

LD_ENV_Spree_Mobile_Production=SPREE_MOBILE_PROD_SDK_KEY
LD_MOBILE_KEY_Spree_Mobile_Production=SPREE_MOBILE_PROD_MOBILE_KEY
LD_ENV_Spree_Webapp_Production=SPREE_WEB_PROD_SDK_KEY
LD_CLIENT_SIDE_ID_Spree_Webapp_Production=SPREE_WEB_PROD_ENV_ID
```

Once a mobile key or environment ID has been configured, you may set the `baseUri` parameter to the host and port of your Relay Proxy instance in your mobile/client-side SDKs. If you are exposing any of the client-side relay endpoints externally, HTTPS should be configured with a TLS termination proxy.


## Event forwarding

The Relay Proxy can also be used to forward events to `events.launchdarkly.com` (unless you have specified a different URL for the events service in your configuration). When enabled, the Relay Proxy will buffer and forward events posted to `/bulk` to the corresponding endpoint in the events service. The primary use case for this is PHP environments, where the performance of a local proxy makes it possible to synchronously flush analytics events. To set up event forwarding, follow one of these examples:

```
# Configuration file example

[Events]
    sendEvents = true
    flushIntervalSecs = 5
    samplingInterval = 0
    capacity = 1000
    inlineUsers = false
```

```
# Environment variables example

USE_EVENTS=true
EVENTS_FLUSH_INTERVAL=5
EVENTS_SAMPLING_INTERVAL=0
EVENTS_CAPACITY=1000
```

This configuration will buffer events for all environments specified in the configuration. The events will be flushed every `flushIntervalSecs`. To point our SDKs to the Relay Proxy for event forwarding, set the `eventsUri` in the SDK to the host and port of your relay instance (or preferably, the host and port of a load balancer fronting your relay instances). Setting `inlineUsers` to `true` preserves full user details in every event (the default is to send them only once per user in an `"index"` event).


## Persistent storage

You can configure Relay Proxy nodes to persist feature flag settings in Redis, DynamoDB, or Consul. This provides durability in case of (e.g.) a temporary network partition that prevents the Relay Proxy from communicating with LaunchDarkly's servers. See [Using a persistent feature store](https://docs.launchdarkly.com/v2.0/docs/using-a-persistent-feature-store).

```
# Configuration file examples

[Redis]
    host = "localhost"
    port = 6379
    localTtl = 30000

[DynamoDB]
    tableName = "my-feature-flags"
    localTtl = 30000

[Consul]
    host = "localhost"
    localTtl = 30000
```

```
# Environment variables examples

USE_REDIS=1
REDIS_HOST=localhost
REDIS_PORT=6379
CACHE_TTL=30000

USE_DYNAMODB=1
DYNAMODB_TABLE=my-feature-flags
CACHE_TTL=30000

USE_CONSUL=1
CONSUL_HOST=localhost
CACHE_TTL=30000
```

Note that the Relay Proxy can only use _one_ of these at a time; for instance, enabling both Redis and DynamoDB is an error.

Also note that the LaunchDarkly SDK clients have their own options for configuring persistent storage. If you are using daemon mode (see below) then the clients need to be using the same storage configuration as the Relay Proxy. If you are not using daemon mode, then the two configurations are completely independent, e.g. you could have a relay using Redis, but a client using Consul or not using persistent storage at all.

In case the database becomes unavailable, Relay's behavior (based on its use of the Go SDK) depends on the `CACHE_TTL` setting:

- If the TTL is a positive number, then the last known flag data will remain cached in memory for that amount of time, after which Relay will be unable to serve flags to SDK clients. Once the database becomes available again, Relay will request all of the flags from LaunchDarkly again and write the latest values to the database.
- If the TTL is a negative number, then the in-memory cache never expires. Relay will continue serving flags to SDK clients, and will update the cache if it receives any flag updates from LaunchDarkly. As Relay will only read from the database upon service startup, it is recommended that you avoid restarting Relay while detecting database downtime. Once the database becomes available again, Relay will write the contents of the cache back to the database. Use the "cached forever" mode with caution: it means that in a scenario where multiple Relay processes are sharing the database, and the current process loses connectivity to LaunchDarkly while other processes are still receiving updates and writing them to the database, the current process will have stale data.

Note that the in-memory cache only helps SDKs using the Relay in proxy mode. SDKs configured to use daemon mode are connected to read directly from the database. [Learn more.](https://docs.launchdarkly.com/docs/using-the-relay-proxy#section-using-the-relay-proxy-in-different-modes)

## Relay proxy mode

The Relay Proxy is typically deployed in relay proxy mode. In this mode, several Relay Proxy instances are deployed in a high-availability configuration behind a load balancer. Relay Proxy nodes do not need to communicate with each other, and there is no master or cluster. This makes it easy to scale the Relay Proxy horizontally by deploying more nodes behind the load balancer.

![Relay Proxy with load balancer](relay-lb.png)


## Daemon mode

Optionally, you can configure our SDKs to communicate directly to the persistent store. If you go this route, there is no need to put a load balancer in front of the Relay Proxy; we call this daemon mode. This is the preferred way to use LaunchDarkly with PHP (as there's no way to maintain persistent stream connections in PHP).

![Relay Proxy in daemon mode](relay-daemon.png)

In this example, the persistent store is in Redis. To set up the Relay Proxy in this mode, provide a Redis host and port, and supply a Redis key prefix for each environment in your configuration:

```
# Configuration file example

[Redis]
    host = "localhost"
    port = 6379
    localTtl = 30000

[Environment "Spree Project Production"]
    prefix = "ld:spree:production"
    sdkKey = "SPREE_PROD_SDK_KEY"

[Environment "Spree Project Test"]
    prefix = "ld:spree:test"
    sdkKey = "SPREE_TEST_SDK_KEY"
```

```
# Environment variables example

USE_REDIS=1
REDIS_HOST=localhost
REDIS_PORT=6379
CACHE_TTL=30000
LD_ENV_Spree_Project_Production=SPREE_PROD_SDK_KEY
LD_PREFIX_Spree_Project_Production=ld:spree:production
LD_ENV_Spree_Project_Test=SPREE_TEST_SDK_KEY
LD_PREFIX_Spree_Project_Test=ld:spree:test
```

(The per-environment "prefix" setting can be used the same way with Consul or DynamoDB. Alternately, with DynamoDB you can use a separate table name for each environment.)

The `localTtl`/`CACHE_TTL` parameter controls the length of time (in milliseconds) that the Relay Proxy will cache data in memory so that feature flag requests do not always hit the database; see [persistent storage](#persistent-storage).

You will then need to [configure your SDK](https://docs.launchdarkly.com/docs/using-a-persistent-feature-store#section-using-a-persistent-feature-store-without-connecting-to-launchdarkly) to connect to Redis directly.


## Flag evaluation endpoints

If you're building an SDK for a language which isn't officially supported by LaunchDarkly, or would like to evaluate feature flags internally without an SDK instance, the Relay Proxy provides endpoints for evaluating all feature flags for a given user. These endpoints support the GET and REPORT http verbs to pass in users either as base64url encoded path parameters, or in the request body, respectively.

Example `curl` requests (default local URI and port):

```shell
curl -X GET -H "Authorization: YOUR_SDK_KEY" localhost:8030/sdk/eval/users/eyJrZXkiOiAiYTAwY2ViIn0=

curl -X REPORT localhost:8030/sdk/eval/user -H "Authorization: YOUR_SDK_KEY" -H "Content-Type: application/json" -d '{"key": "a00ceb", "email":"barnie@example.org"}'
```


## Performance, scaling, and operations

We have done extensive load tests on the Relay Proxy in AWS/EC2. We have also collected a substantial amount of data based on real-world customer use. Based on our experience, we have several recommendations on how to best deploy, operate, and scale the Relay Proxy:

* Networking performance is paramount. Memory and CPU are not as critical. The Relay Proxy should be deployed on boxes with good networking performance. On EC2, we recommend using an instance with [Moderate to High networking performance](http://www.ec2instances.info/) such as `m4.xlarge`. On an `m4.xlarge` instance, a single Relay Proxy node can easily manage 20,000 concurrent connections.

* If using an Elastic Load Balancer in front of the Relay Proxy, you may need to [pre-warm](https://aws.amazon.com/articles/1636185810492479) the load balancer whenever connections to the Relay Proxy are cycled. This might happen when you deploy a large number of new servers that connect to the Relay Proxy, or upgrade the Relay Proxy itself.


## Health check

The Relay Proxy has an additional `status` endpoint which provides the current status of all of its streaming connections. This can obtained by querying the URL path `/status` with a GET request.


## Logging

Like the Go SDK, the Relay Proxy supports four logging levels: Debug, Info, Warn, and Error, with Debug being the most verbose. Setting the minimum level to Info (the default) means Debug is disabled; setting it to Warn means Debug and Info are disabled; etc.

There are two categories of log output: global messages and per-environment messages. Global messages are from the general Relay Proxy infrastructure - for instance, when it has successfully started up, or when it has received an HTTP request. Per-environment messages are for the Relay Proxy's interaction with LaunchDarkly for a specific one of your configured environments - for instance, receiving a flag update or sending analytics events. These can be configured separately: the `logLevel` parameter in `[main]` or the `LOG_LEVEL` variable sets the minimum level for global messages, and the `logLevel` parameter in `[environment]` or the `LD_LOG_LEVEL_envName` variable sets the minimum level for per-environment messages in a specific environment. This is because you may wish to see more verbose output in one category than another, or in one environment than another. If you do not specify a log level for an individual environment, it defaults to the global log level.

Note that debug-level logging for per-environment messages may include user properties and feature flag keys.


## Proxied endpoints

The table below describes the endpoints proxied by the Relay Proxy.  In this table:

* *user* is the base64 representation of a user JSON object (e.g. `{"key": "user1"}` => `eyJrZXkiOiAidXNlcjEifQ==`).
* *clientId* is the 32-hexdigit Client-side ID (e.g. `6488674dc2ea1d6673731ba2`)
* "Auth Header" indicates whether the HTTP request should have an `Authorization` header that is equal to the SDK key, the mobile key, or neither.

Endpoint                           | Method        | Auth Header | Description
-----------------                  |:-------------:|:-----------:| -----------
/sdk/eval/*clientId*/users/*user*  | GET           | n/a         | Returns flag evaluation results for a user
/sdk/eval/*clientId*/users         | REPORT        | n/a         | Same as above but request body is user JSON object
/sdk/evalx/*clientId*/users/*user* | GET           | n/a         | Returns flag evaluation results and additional metadata
/sdk/evalx/*clientId*/users        | REPORT        | n/a         | Same as above but request body is user JSON object
/sdk/flags                         | GET           | sdk         | For [PHP SDK](#using-with-php)
/sdk/flags/*flagKey*               | GET           | sdk         | For [PHP SDK](#using-with-php)
/sdk/segments/*segmentKey*         | GET           | sdk         | For [PHP SDK](#using-with-php)
/sdk/goals/*clientId*              | GET           | n/a         | For JS and other client-side SDKs
/mobile                            | POST          | mobile      | For receiving events from mobile SDKs
/mobile/events                     | POST          | mobile      | Same as above
/mobile/events/bulk                | POST          | mobile      | Same as above
/mobile/events/diagnostic          | POST          | mobile      | Same as above
/bulk                              | POST          | sdk         | For receiving events from server-side SDKs
/diagnostic                        | POST          | sdk         | Same as above
/events/bulk/*clientId*            | POST, OPTIONS | n/a         | For receiving events from JS and other client-side SDKs
/events/diagnostic/*clientId*      | POST, OPTIONS | n/a         | Same as above
/a/*clientId*.gif?d=*events*       | GET, OPTIONS  | n/a         | Same as above
/all                               | GET           | sdk         | SSE stream for all data
/flags                             | GET           | sdk         | Legacy SSE stream for flag data
/ping                              | GET           | sdk         | SSE endpoint that issues "ping" events when there are flag data updates
/ping/*clientId*                   | GET           | n/a         | Same as above but with JS and client-side authorization.
/mping                             | GET           | mobile      | SSE endpoint that issues "ping" events when flags should be re-evaluated
/meval/*user*                      | GET           | mobile      | SSE stream of "ping" and other events for mobile clients
/meval                             | REPORT        | mobile      | Same as above but request body is user JSON object
/eval/*clientId*/*user*            | GET           | n/a         | SSE stream of "ping" and other events for JS and other client-side SDK listeners
/eval/*clientId*                   | REPORT        | n/a         | Same as above but request body is user JSON object


## Exporting metrics and traces

The Relay Proxy may be configured to export statistics and route traces to Datadog, Stackdriver, and Prometheus. See the [configuration section](#configuration-file-format-and-environment-variables) for configuration instructions.

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


## Using with PHP

The [PHP SDK](https://github.com/launchdarkly/php-server-sdk) communicates differently with LaunchDarkly than the other SDKs because it does not support long-lived streaming connections. It must either poll for flags on demand via HTTP, or get them from Redis or another database. The latter is much more efficient and is therefore the preferred approach, but if you are not using a database, the Relay Proxy can handle HTTP requests from PHP.

However, it is highly recommended that if you do this, you use the `ttlMinutes` parameter in the [environment configuration](#file-section-environment-name). This is equivalent to the [TTL setting for the environment on your LaunchDarkly dashboard](https://docs.launchdarkly.com/docs/environments#section-ttl-settings), but must be set here separately because the Relay Proxy does not have access to those dashboard properties. This will cause HTTP responses from the PHP endpoints to have a `Cache-Control: max-age` so that the PHP SDK will not make additional HTTP requests for the same flag more often than that interval. Note that this may result in different PHP application instances receiving flag updates at slightly different times as their HTTP caches will not be exactly in sync. It does not affect any SDKs other than PHP.


## Docker

Using Docker is not required, but if you prefer using a Docker container we provide a Docker entrypoint to make this as easy as possible.

To build the `ld-relay` container:
```
$ docker build -t ld-relay .
```

In Docker, the config file is expected to be found at `/ldr/ld-relay.conf` unless you are using environment variables to configure the Relay Proxy (see the [configuration section](#configuration-file-format-and-environment-variables)).


### Docker examples
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


## Windows

To register the Relay Proxy as a service, run a command prompt as Administrator:
```shell
$ sc create ld-relay DisplayName="LaunchDarkly Relay Proxy" start="auto" binPath="C:\path\to\ld-relay.exe -config C:\path\to\ld-relay.conf"
```


## Integrating the Relay Proxy into your own application

You can also use the Relay Proxy to handle endpoints in your own application if you don't want to use the default `ld-relay` application.  Below is an
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


## Testing

After installing a compatible version of Go, run `make test` to build and run unit tests. To run integration runs, run `make integration-test`. To run the linter, run `make lint`.
