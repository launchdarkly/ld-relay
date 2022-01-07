# LaunchDarkly Relay Proxy - Service endpoints

[(Back to README)](../README.md)

The Relay Proxy provides two different types of service endpoints.

Some service endpoints are proxies for LaunchDarkly services. These correspond to endpoints with the same paths that are at:

* `https://app.launchdarkly.com` 
* `https://stream.launchdarkly.com`
* `https://clientstream.launchdarkly.com`, or 
* `https://events.launchdarkly.com` 

In the tables below, `proxied subdomain` refers to which of those LaunchDarkly service hostnames would normally provide the endpoint.

Others are for functionality that is specific to the Relay Proxy.

## Specific to Relay Proxy

### Status (health check)

Making a `GET` request to the URL path `/status` provides JSON information about the Relay Proxy's configured environments. There is no authentication required for this request.

```json
{
  "environments": {
    "environment1": {
      "sdkKey": "sdk-********-****-****-****-*******99999",
      "envId": "999999999999999999999999",
      "mobileKey": "mob-********-****-****-****-*******99999",
      "status": "connected",
      "connectionStatus": {
        "state": "VALID",
        "stateSince": 10000000
      },
      "dataStoreStatus": {
        "state": "VALID",
        "stateSince": 10000000,
        "database": "redis",
        "dbServer": "redis://my-redis-host",
        "dbPrefix": "env1"
      },
      "bigSegmentStatus": {
        "potentiallyStale": true,
        "lastSynchronizedOn": 1618859993000
       }
    },
    "environment2": {
      "sdkKey": "sdk-********-****-****-****-*******99999",
      "envId": "999999999999999999999999",
      "mobileKey": "mob-********-****-****-****-*******99999",
      "status": "connected",
      "connectionStatus": {
        "state": "INTERRUPTED",
        "stateSince": 12000000,
        "lastError": {
          "kind": "NETWORK_ERROR",
          "time": 12000000
        },
      },
      "bigSegmentStatus": {
        "available": true,
        "potentiallyStale": true,
        "lastSynchronizedOn": 1618859993000
      },
      "dataStoreStatus": {
        "state": "VALID",
        "stateSince": 10000000,
        "database": "dynamodb",
        "dbTable": "env1"
      }
    }
  },
  "status": "healthy",
  "version": "5.11.1",
  "clientVersion": "4.17.2"
}
```

The status properties are defined as follows:

- The `status` for each environment is `"connected"` if the Relay Proxy was able to establish a LaunchDarkly connection and get feature flag data for that environment, and is not experiencing a long connection failure now; it is `"disconnected"` if it is experiencing a long connection failure, or if it was never able to connect in the first place.
    - The definition of a "long" connection failure is based on the `disconnectedStatusTime` property in the [configuration](./configuration.md#file-section-main) (which defaults to one minute): the status will become `"disconnected"` if the Relay Proxy has lost its connection to LaunchDarkly for at least that amount of time consecutively. Some short-lived service interruptions are normal, so the `disconnectedStatusTime` threshold helps to avoid prematurely reporting a disconnected status.
- The `connectionStatus` properties provide more detailed information about the current connectivity to LaunchDarkly.
    - For `state`, `"VALID"` means that the connection is currently working; `"INITIALIZING"` means that it is still starting up; `"INTERRUPTED"` means that it is currently having a problem; `"OFF"` means that it has permanently failed (which only happens if the SDK key is invalid).
    - The `stateSince` property, which is a Unix time measured in milliseconds, indicates how long ago the state changed (so for instance if it is `INTERRUPTED`, this is the time when the connection went from working to not working). 
    - The `lastError` indicates the nature of the most recent failure, with a `kind` that is one of the constants defined by the Go SDK's [DataSourceErrorKind](https://pkg.go.dev/gopkg.in/launchdarkly/go-server-sdk.v5/interfaces?tab=doc#DataSourceErrorKind).
- The `dataStoreStatus` properties are, for the most part, only relevant if you are using [persistent storage](./persistent-storage.md).
    - `state` is `"VALID"` if the last database operation succeeded, or `"INTERRUPTED"` if it failed. If you are not using persistent storage, this is always `VALID` since there is no way for in-memory storage to fail, but the property is provided anyway so you can simply check for a non-`VALID` state to detect problems regardless of how the Relay Proxy is configured.
    - In an `INTERRUPTED` state, the Relay Proxy will continue attempting to contact the database and as soon as it succeeds, the state will change back to `VALID`.
    - `stateSince`, which is a Unix time measured in milliseconds, indicated how long ago `state` changed from `VALID` to `INTERRUPTED` or vice versa.
    - `database`, if present, will be `"redis"`, `"consul"`, or `"dynamodb"`. (In the example above, the two environments are using two different databases; that's not currently possible in Relay, so this is only meant to show what the properties might look like for different configurations.)
    - `dbServer`, if present, is the configured database URL or hostname.
    - `dbPrefix`, if present, is the configured database key prefix for this environment.
    - `dbTable`, if present, is the DynamoDB table name for this environment.
- The `bigSegmentStatus` properties are relevant if you are utilizing Big Segments.
    - `available` is a boolean that is `true` if the database being used for Big Segments seems to be working, or `false` if the most recent database operation failed.
    - `potentiallyStale` is a boolean that indicates if Big Segments are potentially not fully synchronized. This might be because initial synchronization has not completed, or due to a networking error.
    - `lastSynchronizedOn` indicates the last time in Unix milliseconds that Relay can be sure Big Segments were synchronized. Active but incomplete synchronization does not update this timestamp.
- The top-level `status` property for the entire Relay Proxy is `"healthy"` if all of the environments are `"connected"`, or `"degraded"` if any of the environments is `"disconnected"`.
    - In [automatic configuration mode](../configuration.md#file-section-autoconfig), this value can also be `"degraded"` if the Relay Proxy is still starting up and has not yet received environment configurations from LaunchDarkly.
    - When Big Segments are enabled, this value will also be `"degraded"` if the Big Segments status has an `available` property of `false` (indicating a database error), or if `potentiallyStale` is `true` (meaning Big Segments are potentially not fully synchronized) _and_ the configuration setting `bigSegmentsStaleAsDegraded` is enabled.
- `version` is the version of the Relay Proxy.
- `clientVersion` is the version of the Go SDK that the Relay Proxy is using.

The JSON property names within `"environments"` (`"environment1"` and `"environment2"` in this example) are normally the environment names as defined in the Relay Proxy configuration. When using Relay Proxy Enterprise in automatic configuration mode, these will instead be the same as the `envId`, since the environment names may not always stay the same.

### Special flag evaluation endpoints

If you're building an SDK for a language which isn't officially supported by LaunchDarkly, or want to evaluate feature flags internally without an SDK instance, the Relay Proxy provides endpoints for evaluating all feature flags for a given user.

These are equivalent to the polling endpoints for client-side/mobile SDKs, except that they use the SDK key as a credential rather than the mobile key or client-side environment ID.

Endpoint                  | Method   | Description
--------------------------|:--------:|------------------------------------
`/sdk/eval/users/{user}`  | `GET`    | Evaluates all flag values for the given user
`/sdk/eval/user`          | `REPORT` | Same as above but request body is user JSON object
`/sdk/evalx/users/{user}` | `GET`    | Same as `/sdk/eval/users/{user}`, but provides additional metadata such as evaluation reason
`/sdk/evalx/user`         | `REPORT` | Same as above but request body is user JSON object

Example `curl` requests (default local URI and port):

```shell
curl -X GET -H "Authorization: YOUR_SDK_KEY" localhost:8030/sdk/eval/users/eyJrZXkiOiAiYTAwY2ViIn0=

curl -X REPORT localhost:8030/sdk/eval/user -H "Authorization: YOUR_SDK_KEY" -H "Content-Type: application/json" -d '{"key": "a00ceb", "email":"barnie@example.org"}'
```


## Proxies for LaunchDarkly services

### Endpoints that server-side SDKs use

All of these require an `Authorization` header whose value is the SDK key.

Endpoint                     | Method | Proxied Subdomain | Description
-----------------------------|:------:|:---------:|------------------------------------
`/all`                       | `GET`  | `stream.` | SSE stream for all data
`/bulk`                      | `POST` | `events.` | Receives analytics events from SDKs
`/diagnostic`                | `POST` | `events.` | Receives diagnostic data from SDKs
`/flags`                     | `GET`  | `stream.` | SSE stream for flag data (older SDKs)
`/sdk/flags`                 | `GET`  | `app.`    | Polling endpoint for [PHP SDK](./php.md)
`/sdk/flags/{key}`           | `GET`  | `app.`    | Polling endpoint for [PHP SDK](./php.md)
`/sdk/segments/{key}`        | `GET`  | `app.`    | Polling endpoint for [PHP SDK](./php.md)

For server-side SDKs other than PHP, the Relay Proxy does not support polling mode, only streaming.

The `GET`/`REPORT` endpoints will return a 401 error if the `Authorization` header does not match an SDK key that is known to the Relay Proxy, just as the actual LaunchDarkly service endpoints would do for an invalid SDK key. They will return a 503 error if the Relay Proxy has not yet successfully obtained feature flag data from LaunchDarkly for the specified environment (either because it is still starting up, or because of a service outage or network interruption). In [automatic configuration mode](../configuration.md#file-section-autoconfig), they will return a 503 error if the Relay Proxy has not yet received its configuration from LaunchDarkly.


### Endpoints that mobile SDKs use

All of these require an `Authorization` header whose value is the mobile key. 

`{user}` is the base64 representation of a user JSON object (e.g. `{"key": "user1"}` => `eyJrZXkiOiAidXNlcjEifQ==`).

Endpoint                     | Method   | Proxied Subdomain | Description
-----------------------------|:--------:|:---------------:|------------------------------------
`/meval/{user}`              | `GET`    | `clientstream.` | SSE stream of "ping" and other events
`/meval`                     | `REPORT` | `clientstream.` | Same as above but request body is user JSON object
`/mobile`                    | `POST`   | `events.`       | For receiving events from mobile SDKs
`/mobile/events`             | `POST`   | `events.`       | Same as above
`/mobile/events/bulk`        | `POST`   | `events.`       | Same as above
`/mobile/events/diagnostic`  | `POST`   | `events.`       | Same as above
`/mping`                     | `GET`    | `clientstream.` | SSE stream for older SDKs that issues "ping" events when flags have changed
`/msdk/eval/users/{user}`    | `GET`    | `app.`          | Polling endpoint, returns flag evaluation results for a user
`/msdk/eval/user`            | `REPORT` | `app.`          | Same as above but request body is user JSON object
`/msdk/evalx/users/{user}`   | `GET`    | `app.`          | Same as `/msdk/eval/users/{user}` for newer SDKs, with additional metadata
`/msdk/evalx/user`           | `REPORT` | `app.`          | Same as above but request body is user JSON object

The `GET`/`REPORT` endpoints will return a 401 error if the `Authorization` header does not match an SDK key that is known to the Relay Proxy, just as the actual LaunchDarkly service endpoints would do for an invalid SDK key. They will return a 503 error if the Relay Proxy has not yet successfully obtained feature flag data from LaunchDarkly for the specified environment (either because it is still starting up, or because of a service outage or network interruption). In [automatic configuration mode](../configuration.md#file-section-autoconfig), they will return a 503 error if the Relay Proxy has not yet received its configuration from LaunchDarkly.


### Endpoints that client-side JavaScript SDKs use

`{envId}` is the 32-hexdigit client-side environment ID (e.g. `6488674dc2ea1d6673731ba2`).

`{user}` is the base64 representation of a user JSON object (e.g. `{"key": "user1"}` => `eyJrZXkiOiAidXNlcjEifQ==`).

These endpoints also support the `OPTION` method to enable CORS requests from browsers.

Endpoint                          | Method   | Proxied Subdomain | Description
-------------------------------------|:--------:|:---------------:|------------------------------------
`/a/{envId}.gif?d=*events*`       | `GET`    | `events.`       | Alternative analytics event mechanism used if browser does not allow CORS
`/eval/{envId}/{user}`            | `GET`    | `clientstream.` | SSE stream of "ping" and other events for JS and other client-side SDK listeners
`/eval/{envId}`                   | `REPORT` | `clientstream.` | Same as above but request body is user JSON object
`/events/bulk/{envId}`            | `POST`   | `events.`       | Receives analytics events from SDKs
`/events/diagnostic/{envId}`      | `POST`   | `events.`       | Receives diagnostic data from SDKs
`/ping/{envId}`                   | `GET`    | `clientstream.` | SSE stream for older SDKs that issues "ping" events when flags have changed
`/sdk/eval/{envId}/users/{user}`  | `GET`    | `app.`          | Polling endpoint for older SDKs, returns flag evaluation results for a user
`/sdk/eval/{envId}/users`         | `REPORT` | `app.`          | Same as above but request body is user JSON object
`/sdk/evalx/{envId}/users/{user}` | `GET`    | `app.`          | Polling endpoint, returns flag evaluation results and additional metadata
`/sdk/evalx/{envId}/users`        | `REPORT` | `app.`          | Same as above but request body is user JSON object
`/sdk/goals/{envId}`              | `GET`    | `app.`          | Provides goals data used by JS SDK

The `GET`/`REPORT` endpoints return a 404 error if the environment ID is not recognized by Relay. This is different from the server-side and mobile endpoints, which return 401 for an unrecognized credential; it is consistent with the behavior of the corresponding LaunchDarkly service endpoints for client-side JavaScript SDKs.
