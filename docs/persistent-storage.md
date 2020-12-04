# LaunchDarkly Relay Proxy - Persistent storage

[(Back to README)](../README.md)

You can configure Relay Proxy nodes to persist feature flag settings in Redis, DynamoDB, or Consul. This provides durability in use cases like a temporary network partition that prevents the Relay Proxy from communicating with LaunchDarkly's servers.

To learn more, read [Using a persistent feature store](https://docs.launchdarkly.com/sdk/concepts/feature-store), and the Relay Proxy documentation on [Configuration](./configuration.md).

The Relay Proxy does not currently support clustered Redis or Redis Sentinel.

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

It's important to understand that the Relay Proxy can only use _one_ of these at a time. If you enabled both Redis and DynamoDB, for example, it would result in an error.

LaunchDarkly SDK clients have their own options for configuring persistent storage. If you use [daemon mode](../README.md#daemon-mode), the clients need to be using the same storage configuration as the Relay Proxy. If you are not using daemon mode, the two configurations are completely independent. For example, you could have a relay using Redis, but a client using Consul or not using persistent storage at all.

If the database becomes unavailable, Relay's behavior (based on its use of the Go SDK) depends on the `CACHE_TTL` setting:

- If the TTL is a positive number, the last known flag data will remain cached in memory for that amount of time, after which Relay will be unable to serve flags to SDK clients. After the database becomes available again, Relay will request all of the flags from LaunchDarkly again and write the latest values to the database.
- If the TTL is a negative number, the in-memory cache never expires. Relay continues to serve flags to SDK clients and updates the cache if it receives any flag updates from LaunchDarkly. As Relay will only read from the database upon service startup, it is recommended that you avoid restarting Relay while detecting database downtime. After the database becomes available again, Relay will write the contents of the cache back to the database. Use the "cached forever" mode with caution: it means that in a scenario where multiple Relay processes are sharing the database, and the current process loses connectivity to LaunchDarkly while other processes are still receiving updates and writing them to the database, the current process will have stale data.

The in-memory cache only helps SDKs using the Relay in proxy mode. SDKs configured to use daemon mode are connected to read directly from the database. To learn more, read [Using the Relay Proxy in different modes](https://docs.launchdarkly.com/home/advanced/relay-proxy/using#using-the-relay-proxy-in-different-modes).
