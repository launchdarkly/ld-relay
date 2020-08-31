# LaunchDarkly Relay Proxy - Configuration

[(Back to README)](../README.md)

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

Every configuration file option has an equivalent environment variable.


### Allowable values for types

For **Boolean** settings, a value of either `true` or `1` is considered true; `false`, `0`, or an empty value is considered false; any other value is invalid.

For **Duration** settings, the value should be be an integer followed by `ms`, `s`, `m`, or `h` for milliseconds, seconds, minutes, or hours (example: `30s` for 30 seconds); or, you can combine these (example: `1m30s`). Specifying a number by itself without a unit is not allowed.

**URI** settings will cause an error if you specify a value that is an invalid URI, or a relative URI.


### File section: `[Main]`

Property in file         | Environment var      | Type    | Default | Description
------------------------ | -------------------- | :-----: | :------ | -----------
`streamUri`              | `STREAM_URI`         | URI     | _(1)_   | URI for the LaunchDarkly streaming service.
`baseUri`                | `BASE_URI`           | URI     | _(1)_   | URI for the LaunchDarkly polling service.
`exitOnError`            | `EXIT_ON_ERROR`      | Boolean | `false` | Close the Relay Proxy if it encounters any error during initialization.
`exitAlways`             | `EXIT_ALWAYS`        | Boolean | `false`  | Close the Relay Proxy immediately after initializing all environments (do not start an HTTP server). _(2)_
`ignoreConnectionErrors` | `IGNORE_CONNECTION_ERRORS` | Boolean | `false` | Ignore any initial connectivity issues with LaunchDarkly. Best used when network connectivity is not reliable.
`port`                   | `PORT`               | Number  | `8030`  | Port the Relay Proxy should listen on.
`heartbeatInterval`      | `HEARTBEAT_INTERVAL` | Number  | `3m`    | Interval for heartbeat messages to prevent read timeouts on streaming connections. Assumed to be in seconds if no unit is specified.
`maxClientConnectionTime` | `MAX_CLIENT_CONNECTION_TIME` | Duration | none | Maximum amount of time that Relay will allow a streaming connection from an SDK client to remain open. _(3)_
`disconnectedStatusTime` | `DISCONNECTED_STATUS_TIME` | Duration | `1m` | How long a stream connection can be interrupted before Relay reports the status as "disconnected". _(4)_
`disableUsageMetrics`    | `DISABLE_USAGE_METRICS` | Boolean | `false` | Turn off the sending of usage statistics to LaunchDarkly. _(5)_
`tlsEnabled`             | `TLS_ENABLED`        | Boolean | `false` | Enable TLS on the Relay Proxy. **See: [Using TLS](./tls.md)**
`tlsCert`                | `TLS_CERT`           | String  |         | Required if `tlsEnabled` is true. Path to TLS certificate file.
`tlsKey`                 | `TLS_KEY`            | String  |         | Required if `tlsEnabled` is true. Path to TLS private key file.
`tlsMinVersion`          | `TLS_MIN_VERSION`    | String  |         | Set to "1.2", etc., to enforce a minimum TLS version for secure requests.
`logLevel`               | `LOG_LEVEL`          | String  | `info`  | Should be `debug`, `info`, `warn`, `error`, or `none`. **See: [Logging](./logging.md)**

_(1)_ The default values for `streamUri` and `baseUri` are `https://app.launchdarkly.com` and `https://stream.launchdarkly.com`. You should never need to change these URIs unless a) you are using a special instance of the LaunchDarkly service, in which case support will tell you how to set them, or b) you are accessing LaunchDarkly via a reverse proxy or some other mechanism that rewrites URLs.

_(2)_ The `exitAlways` mode is intended for use cases where you do not want to maintain a long-running Relay Proxy instance, but only execute it at specific times to get flags; this is only useful if you have enabled Redis or another database, so that it will store the flags there.

_(3)_ The optional `maxClientConnectionTime` setting may be useful in load-balanced environments, to avoid having stream connections pile up excessively on one instance when other instances are removed or restarted. If you tell Relay to automatically close every stream connection after some amount of time, this will cause the SDK client that made the connection to reconnect, so that the load balancer can potentially direct it to a different instance.

_(4)_ For details about `disconnectedStatusTime`, see: [Service endpoints - Status (health check)](./endpoints.md#status-health-check)

_(5)_ The `disableUsageMetrics` option applies to metrics that LaunchDarkly normally gathers to determine what types and versions of SDKs are being used with the Relay Proxy, as well as some diagnostic information that is normally gathered by the Go SDK describing the OS platform and version that the Relay Proxy is being run on and whether a database is being used. This does not affect the ability to export metrics to Datadog, Stackdriver, or Prometheus.

### File section: `[Events]`

To learn more, read [Forwarding events](./events.md)

Property in file    | Environment var            | Type    | Default | Description
------------------- | -------------------------- | :-----: | :------ | -----------
`sendEvents`        | `USE_EVENTS`               | Boolean | `false` | When enabled, LD-Relay will send analytic events it receives to LaunchDarkly.
`eventsUri`         | `EVENTS_HOST`              | URI     | _(4)_   | URI for the LaunchDarkly events service
`flushInterval`     | `EVENTS_FLUSH_INTERVAL`    | Duration | `5s`   | Controls how long the SDK buffers events before sending them back to our server. If your server generates many events per second, we suggest decreasing the flush interval and/or increasing capacity to meet your needs.
`capacity`          | `EVENTS_CAPACITY`          | Number  | `1000`  | Maximum number of events to accumulate for each flush interval.
`inlineUsers`       | `EVENTS_INLINE_USERS`      | Boolean | `false` | When enabled, individual events (if full event tracking is enabled for the feature flag) will contain all non-private user attributes.

_(4)_ See note _(1)_ above. The default value for `eventsUri` is `https://events.launchdarkly.com`.


### File section: `[Environment "NAME"]`

The Relay Proxy allows you to proxy any number of LaunchDarkly environments; there must be at least one. In a configuration file, each of these is a separate section in the format `[Environment "MyEnvName"]`, where `MyEnvName` is a unique identifier for the environment (this does not have to match the environment name on your LaunchDarkly dashboard, but it is recommended to). If you are using environment variables, you will add the `MyEnvName` identifier to the variable name prefix for each property. See examples below.

Property in file | Environment var               | Type   | Description
---------------- | ----------------------------- | :----: | -----------
`sdkKey`         | `LD_ENV_MyEnvName`            | String | Server-side SDK key for the environment. Required.
`mobileKey`      | `LD_MOBILE_KEY_MyEnvName`     | String | Mobile key for the environment. Required if you are proxying mobile SDK functionality.
`envId`          | `LD_CLIENT_SIDE_ID_MyEnvName` | String | Client-side ID for the environment. Required if you are proxying client-side JavaScript-based SDK functionality.
`secureMode`     | `LD_SECURE_MODE_MyEnvName`    | Boolean | True if [secure mode](https://docs.launchdarkly.com/sdk/client-side/javascript#secure-mode) should be required for client-side JS SDK connections.
`prefix`         | `LD_PREFIX_MyEnvName`         | String | If using a Redis, Consul, or DynamoDB feature store, this string will be added to all database keys to distinguish them from any other environments that are using the database.
`tableName`      | `LD_TABLE_NAME_MyEnvName`     | String | If using DynamoDB, you can specify a different table for each environment. (Or, specify a single table in the `[DynamoDB]` section and use `prefix` to distinguish the environments.)
`allowedOrigin`  | `LD_ALLOWED_ORIGIN_MyEnvName` | URI    | If provided, adds CORS headers to prevent access from other domains. This variable can be provided multiple times per environment (if using the `LD_ALLOWED_ORIGIN_MyEnvName` variable, specify a comma-delimited list).
`logLevel`       | `LD_LOG_LEVEL_MyEnvName`      | String | Should be `debug`, `info`, `warn`, `error`, or `none`. **See: [Logging](./logging.md)**
`ttl`            | `LD_TTL_MyEnvName`            | Duration | HTTP caching TTL for the PHP polling endpoints. **See: [Using PHP](./php.md)**

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


### File section: `[Redis]`

To learn more, read [Persistent storage](./persistent-storage.md).

Property in file | Environment var  | Type    | Default | Description
---------------- | ---------------- | :-----: | :------ | -----------
n/a              | `USE_REDIS`      | Boolean | `false`     | If you are using environment variables, set this to enable Redis.
`host`           | `REDIS_HOST`     | String  | `localhost` | Hostname of the Redis database. Redis is enabled if this or `url` is set.
`port`           | `REDIS_PORT`     | Number  | `6379`      | Port of the Redis database. Note that if you are using environment variables, setting `REDIS_PORT` to a string like `tcp://host:port` sets both the host and the port; this is used in Docker.
`url`            | `REDIS_URL`      | String  |             | URL of the Redis database (overrides `host` & `port`).
`tls`            | `REDIS_TLS`      | Boolean | `false`     | If `true`, will use a secure connection to Redis (not all Redis servers support this). If you specified a `redis://` URL, setting `tls` to `true` will change it to `rediss://`.
`password`       | `REDIS_PASSWORD` | String  |             | Optional password if Redis require authentication.
`localTtl`       | `CACHE_TTL`      | Duration | `30s`      | Length of time that database items can be cached in memory.

Note that the TLS and password options can also be specified as part of the URL: `rediss://` instead of `redis://` enables TLS, and `redis://:password@host` instead of `redis://host` sets a password. You may want to use the separate options instead if, for instance, you want your configuration file to contain the basic Redis configuration, but for security reasons you would rather set the password in an environment variable (`REDIS_PASSWORD`).


### File section: `[DynamoDB]`

To learn more, read [Persistent storage](./persistent-storage.md).

Property in file    | Environment var    | Type    | Default | Description
------------------- | ------------------ | :-----: | :------ | -----------
`enabled`           | `USE_DYNAMODB`     | Boolean | `false` | Enables DynamoDB.
`tableName`         | `DYNAMODB_TABLE`   | String  |         | The DynamoDB table name, if you are using the same table for all environments. Otherwise, omit this and specify it in each environment section. (Note, credentials and region are controlled by the usual AWS environment variables and/or local AWS configuration files.)
`url`               | `DYNAMODB_URL`     | String  |         | The service endpoint if you are using a local DynamoDB instance instead of the regular service.
`localTtl`          | `CACHE_TTL`        | Duration | `30s`  | Length of time that database items can be cached in memory.

The AWS credentials and region for DynamoDB are not part of the Relay configuration; they should be set using either the standard AWS environment variables or a local AWS configuration file, as documented for [the AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-envvars.html).


### File section: `[Consul]`

To learn more, read [Persistent storage](./persistent-storage.md).

Property in file | Environment var | Type    | Default     | Description
---------------- | --------------- | :-----: | :---------- | -----------
n/a              | `USE_CONSUL`    | Boolean | `false`     | If you are using environment variables, set this to enable Consul.
`host`           | `CONSUL_HOST`   | String  | `localhost` | Hostname of the Consul server. Consul is enabled if this is set.
`localTtl`       | `CACHE_TTL`     | Duration | `30s`      | Length of time that database items can be cached in memory.


### File section: `[Datadog]`

To learn more, read [Metrics integrations](./metrics.md)

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

To learn more, read [Metrics integrations](./metrics.md)

Property in file | Environment var          | Type    | Default | Description
---------------- | ------------------------ | :-----: | :------ | -----------
`enabled`        | `USE_STACKDRIVER`        | Boolean | `false` | If true, enables exporting metrics and traces to Stackdriver.
`projectID`      | `STACKDRIVER_PROJECT_ID` | String  |         | Google cloud project ID.
`prefix`         | `STACKDRIVER_PREFIX`     | String  |         | The metrics prefix to be used by Stackdriver.


### File section: `[Prometheus]`

To learn more, read [Metrics integrations](./metrics.md)

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
`caCertFiles`    | `PROXY_CA_CERTS`      | String  |         | List of file paths to additional CA certificates that should be trusted (in PEM format). For multiple files, if using a configuration file, you can specify `caCertFiles` multiple times; if using environment variables, you can set `PROXY_CA_CERTS` to a comma-delimited list.
`ntlmAuth`       | `PROXY_AUTH_NTLM`     | Boolean | `false` | Enables NTLM proxy authentication (requires user, password, and domain).
