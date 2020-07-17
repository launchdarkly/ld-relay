# LaunchDarkly Relay Proxy - Persistent Storage

[(back to README)](../README.md)

You can configure Relay Proxy nodes to persist feature flag settings in Redis, DynamoDB, or Consul. This provides durability in case of (e.g.) a temporary network partition that prevents the Relay Proxy from communicating with LaunchDarkly's servers.

See the reference guide for [Using a persistent feature store](https://docs.launchdarkly.com/sdk/concepts/feature-store), and the Relay Proxy documentation on [Configuration](./configuration.md).

```
# Configuration file examples

[Redis]
    host = "localhost"
    port = 6379
    localTtl = 30s

[DynamoDB]
    tableName = "my-feature-flags"
    localTtl = 30s

[Consul]
    host = "localhost"
    localTtl = 30s
```

```
# Environment variables examples

USE_REDIS=1
REDIS_HOST=localhost
REDIS_PORT=6379
CACHE_TTL=30s

USE_DYNAMODB=1
DYNAMODB_TABLE=my-feature-flags
CACHE_TTL=30s

USE_CONSUL=1
CONSUL_HOST=localhost
CACHE_TTL=30s
```

Note that the Relay Proxy can only use _one_ of these at a time; for instance, enabling both Redis and DynamoDB is an error.

Also note that the LaunchDarkly SDK clients have their own options for configuring persistent storage. If you are using [daemon mode](../README.md#daemon-mode), then the clients need to be using the same storage configuration as the Relay Proxy. If you are not using daemon mode, then the two configurations are completely independent, e.g. you could have a relay using Redis, but a client using Consul or not using persistent storage at all.

In case the database becomes unavailable, Relay's behavior (based on its use of the Go SDK) depends on the `CACHE_TTL` setting:

- If the TTL is a positive number, then the last known flag data will remain cached in memory for that amount of time, after which Relay will be unable to serve flags to SDK clients. Once the database becomes available again, Relay will request all of the flags from LaunchDarkly again and write the latest values to the database.
- If the TTL is a negative number, then the in-memory cache never expires. Relay will continue serving flags to SDK clients, and will update the cache if it receives any flag updates from LaunchDarkly. As Relay will only read from the database upon service startup, it is recommended that you avoid restarting Relay while detecting database downtime. Once the database becomes available again, Relay will write the contents of the cache back to the database. Use the "cached forever" mode with caution: it means that in a scenario where multiple Relay processes are sharing the database, and the current process loses connectivity to LaunchDarkly while other processes are still receiving updates and writing them to the database, the current process will have stale data.

Note that the in-memory cache only helps SDKs using the Relay in proxy mode. SDKs configured to use daemon mode are connected to read directly from the database. [Learn more.](https://docs.launchdarkly.com/home/advanced/relay-proxy/using#using-the-relay-proxy-in-different-modes)
