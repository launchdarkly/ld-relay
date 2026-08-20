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

## Notes on request/path parameters

* `{contextBase64}` means the JSON representation of an evaluation context converted to base64 encoding.
  * The JSON representation could be either in the current evaluation context JSON format (example: `{"kind": "user", "key": "abc", "attr1": true}`), or the older user JSON format (example: `{"key": "abc", "custom": {"attr1": true}}`).
  * In either case, the JSON data must be encoded using the [base64url](https://datatracker.ietf.org/doc/html/rfc4648#section-5) variant of base64 encoding.
* `{envId}` means the client-side ID of a LaunchDarkly environment (typically a 32-character hexadecimal value, such as `6488674dc2ea1d6673731ba2`).
* `{flagKey}` means the unique key of a feature flag.
* `{segmentKey}` means the unique key of a segment.

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
    - The `lastError` indicates the nature of the most recent failure, with a `kind` that is one of the constants defined by the Go SDK's [DataSourceErrorKind](https://pkg.go.dev/github.com/launchdarkly/go-server-sdk/v7/interfaces?tab=doc#DataSourceErrorKind).
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
    - In [automatic configuration mode](configuration.md#file-section-autoconfig), this value can also be `"degraded"` if the Relay Proxy is still starting up and has not yet received environment configurations from LaunchDarkly.
    - When Big Segments are enabled, this value will also be `"degraded"` if the Big Segments status has an `available` property of `false` (indicating a database error), or if `potentiallyStale` is `true` (meaning Big Segments are potentially not fully synchronized) _and_ the configuration setting `bigSegmentsStaleAsDegraded` is enabled.
- `version` is the version of the Relay Proxy.
- `clientVersion` is the version of the Go SDK that the Relay Proxy is using.

The JSON property names within `"environments"` (`"environment1"` and `"environment2"` in this example) are normally the environment names as defined in the Relay Proxy configuration. When using Relay Proxy Enterprise in automatic configuration mode, these will instead be the same as the `envId`, since the environment names may not always stay the same.

### Per-environment status

For querying the status of a specific environment without fetching data for all environments, the Relay Proxy provides granular status endpoints. These are useful for monitoring specific environments or for debugging. There is no authentication required for these requests.

**Endpoints:**

```
GET /status/{identifier}
GET /status/{identifier}/filters/{filterKey}
GET /status/{projKey}/{envKey}
GET /status/{projKey}/{envKey}/filters/{filterKey}
```

**Identifier formats:**

The `{identifier}` parameter supports different formats depending on your configuration mode:

**Manual configuration mode:**
- **Configured name** (e.g., `My%20Production%20Env`) - The environment name as defined in your Relay Proxy configuration file. URL-encode spaces and special characters.
- **Environment ID** (e.g., `507f1f77bcf86cd799439011`) - Only available if you explicitly configured the `envId` field for the environment (typically used when supporting client-side JavaScript SDKs).

**Automatic configuration mode:**
- **Environment ID** (e.g., `507f1f77bcf86cd799439011`) - Always available. This is a stable identifier, ideal for automation and monitoring scripts.
- **Project/environment key route**: `/status/{projKey}/{envKey}` (e.g., `/status/my-app/production`) - Human-readable hierarchical identifiers.

**Filter support:**

When [payload filters](./configuration.md#payload-filtering) are configured, you can query the status of specific filtered variants:

- `/status/{identifier}/filters/{filterKey}` - Status for a filtered variant by environment ID or configured name
- `/status/{projKey}/{envKey}/filters/{filterKey}` - Status for a filtered variant by project/environment keys

**Response format:**

The response is a single environment status object (not wrapped in an `"environments"` map):

```json
{
  "sdkKey": "sdk-********-****-****-****-*******99999",
  "envId": "507f1f77bcf86cd799439011",
  "envKey": "production",
  "envName": "Production",
  "projKey": "my-app",
  "projName": "My Application",
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
    "dbPrefix": "ld-507f1f77bcf86cd799439011"
  }
}
```

The response structure matches the per-environment data returned by `/status`, with the same properties and meanings described above. The `envKey`, `envName`, `projKey`, and `projName` fields are only present when using automatic configuration mode.

**HTTP status codes:**

- `200 OK` - Environment found and status returned successfully
- `404 Not Found` - The specified environment identifier or filter does not exist
- `503 Service Unavailable` - The Relay Proxy has not yet fully initialized

**Example requests:**

```shell
# Manual config mode - by configured name
curl http://localhost:8030/status/production-env

# Manual config mode - by environment ID (if envId is configured)
curl http://localhost:8030/status/507f1f77bcf86cd799439011

# Auto-config mode - by environment ID
curl http://localhost:8030/status/507f1f77bcf86cd799439011

# Auto-config mode - by project/environment keys
curl http://localhost:8030/status/my-app/production

# With filters (any mode)
curl http://localhost:8030/status/507f1f77bcf86cd799439011/filters/microservice-a
curl http://localhost:8030/status/my-app/production/filters/microservice-a
```

**Use cases:**

- **Monitoring**: Poll specific environments without fetching data for all environments
- **Debugging**: Quickly check the status of a single environment during troubleshooting
- **Filtered environments**: Verify the status of specific payload filter variants

### Health assertions (the `expect` query parameter)

Any of the status endpoints above (`/status` and the per-environment routes) accept an optional `expect` query parameter that lets the Relay Proxy validate its own state and answer with an HTTP status code. This means a monitoring script or health probe can decide whether the Relay Proxy is in the state it expects without fetching the JSON body and parsing it (for example, with `jq`).

Each `expect` clause is a path into the response body, a comparison operator, and an expected value:

```
expect=<path><operator><value>
```

The parameter can be repeated; all clauses must hold for the request to be considered satisfied (they are combined with logical AND).

```shell
# Is the whole Relay Proxy healthy?
curl -fsS 'http://localhost:8030/status?expect=status=healthy'

# Is one specific environment connected and its data source valid?
curl -fsS 'http://localhost:8030/status/my-app/production?expect=status=connected&expect=connectionStatus.state=VALID'
```

With `curl -f`, a non-2xx response makes `curl` exit non-zero, so a shell script can branch on the exit code with no body parsing at all.

**Path syntax:**

- Paths address the JSON body that *that route* returns. On `/status` the body is the full document, so an environment is reached via `environments.<key>.<field>`. On a per-environment route the body is the single environment object, so the same field is just `status` or `connectionStatus.state`.
- The keys under `environments` are the same display names used elsewhere in the `/status` body: normally `"<projName> <envName>"` (with a `" (<filterKey>)"` suffix for a filtered variant), or the environment ID in automatic configuration mode. Because these usually contain spaces and parentheses, bracket-quote the key and URL-encode the clause: `expect=environments["My Application Production"].status=connected`. Querying a per-environment route (for example `/status/my-application/production`) avoids the map key entirely and is usually simpler.
- Use dotted segments for nested objects: `connectionStatus.state`, `bigSegmentStatus.available`.
- For a map key that contains a dot or other punctuation, bracket-quote it: `environments["my.env"].status`.
- Arrays can be addressed by index (`somearray[0].field`) or by matching a field within an element (`somearray[field=value].otherField`). No field of the current status document is an array; this syntax exists so that selectors keep working when one becomes an array, and addressing a field that is not an array today returns `422`.

**Operators and comparison:**

- `=` asserts the value at the path equals the expected value; `!=` asserts it does not. These are the only two operators; anything else (`>=`, `==`, `=~`, ...) returns `422`.
- Comparison is done as strings against the value as it appears in the JSON response (for example `available=true`, `connectionStatus.state=VALID`, or a `stateSince` timestamp such as `stateSince=1618859993000`).

**HTTP status codes:**

Every clause is evaluated, so one request reports everything you need to fix. The response code is the most serious outcome across all of the clauses, in the order below.

- `400 Bad Request` - a clause could not be read as `<path><operator><value>` at all: no operator, no path before the operator, or a malformed `[...]` selector. A present-but-empty value (`?expect=`) is one of these.
- `422 Unprocessable Content` - a clause was read successfully, but names something the Relay Proxy cannot evaluate: an operator other than `=` or `!=`, a field that does not exist in the status document, or a path that stops on an object rather than on a single value.
- `412 Precondition Failed` - every clause was evaluable and at least one did not hold. (`412`, rather than `503`, is used so that an unmet assertion is distinguishable from the Relay Proxy not yet being initialized, which the per-environment routes report as `503`.)
- `200 OK` - every clause held.

On the per-environment routes, an unknown environment or filter (`404`) or an uninitialized Relay Proxy (`503`) is reported before any clause is evaluated.

**`422` and `412` both mean "not what you asked for", so it is worth being precise about which you get.** The difference is whether the *field* exists in the status document, not whether it is present in this particular response:

- `connexionStatus.state=VALID` returns `422`: there is no such field, so the assertion can never be answered. This is almost always a typo.
- `environments.my-env.status=connected` returns `412` when `my-env` is not a configured environment. Any environment key is addressable, so this is a well-formed question whose answer is "no".
- `bigSegmentStatus.available=true` returns `412` on an environment with no big segment store, and `expiringSdkKey=...` returns `412` when no key is expiring. Both fields are real but are omitted when they have no value.

When `expect` is supplied, the response body is a summary of the evaluation rather than the usual status document; the HTTP status code is the contract, and the body is for debugging. Clauses appear in the order you supplied them. A clause that was evaluated reports `expected`, `actual`, and `ok`; a clause that could not be evaluated reports `problem` instead:

```json
{
  "satisfied": false,
  "results": [
    { "expr": "status=healthy", "expected": "healthy", "actual": "degraded", "ok": false },
    { "expr": "connexionStatus.state=VALID", "problem": "unknown field \"connexionStatus\"", "ok": false },
    { "expr": "status>=healthy", "problem": "operator \">=\" is not supported; use \"=\" or \"!=\"", "ok": false }
  ]
}
```

Requests without an `expect` parameter are unaffected and return the full status document as described above.

### Special flag evaluation endpoints

If you're building an SDK for a language which isn't officially supported by LaunchDarkly, or want to evaluate feature flags internally without an SDK instance, the Relay Proxy provides endpoints for evaluating all feature flags for a given user.

These are equivalent to the polling endpoints for client-side/mobile SDKs, except that they use the SDK key as a credential rather than the mobile key or client-side environment ID.

| Endpoint                              |  Method  | Description                                                                           |
|---------------------------------------|:--------:|---------------------------------------------------------------------------------------|
| `/sdk/evalx/contexts/{contextBase64}` |  `GET`   | Evaluates all flag values for the given evaluation context                            |
| `/sdk/evalx/context`                  | `REPORT` | Same as above, but request body is the evaluation context JSON object (not in base64) |
| `/sdk/evalx/users/{contextBase64}`    |  `GET`   | Alternate name for `/sdk/evalx/contexts/{contextBase64}`                              |
| `/sdk/evalx/user`                     | `REPORT` | Alternate name for `/sdk/evalx/context`                                               |

Example `curl` requests (default local URI and port):

```shell
curl -X GET -H "Authorization: YOUR_SDK_KEY" localhost:8030/sdk/evalx/users/eyJraW5kIjogInVzZXIiLCAia2V5IjogImEwMGNlYiIsICJlbWFpbCI6ICJiYXJuaWVAZXhhbXBsZS5vcmcifQ

curl -X REPORT localhost:8030/sdk/evalx/context -H "Authorization: YOUR_SDK_KEY" -H "Content-Type: application/json" -d '{"kind": "user", "key": "a00ceb", "email": "barnie@example.org"}'
```


## Proxies for LaunchDarkly services

### Endpoints that server-side SDKs use

All of these require an `Authorization` header whose value is the SDK key.

| Endpoint                     | Method | Proxied Subdomain | Description                              |
|------------------------------|:------:|:-----------------:|------------------------------------------|
| `/all`                       | `GET`  |     `stream.`     | SSE stream for all data                  |
| `/bulk`                      | `POST` |     `events.`     | Receives analytics events from SDKs      |
| `/diagnostic`                | `POST` |     `events.`     | Receives diagnostic data from SDKs       |
| `/flags`                     | `GET`  |     `stream.`     | SSE stream for flag data (older SDKs)    |
| `/sdk/flags`                 | `GET`  |      `sdk.`       | Polling endpoint for [PHP SDK](./php.md) |
| `/sdk/flags/{flagKey}`       | `GET`  |      `sdk.`       | Polling endpoint for [PHP SDK](./php.md) |
| `/sdk/segments/{segmentKey}` | `GET`  |      `sdk.`       | Polling endpoint for [PHP SDK](./php.md) |

For server-side SDKs other than PHP, the Relay Proxy does not support polling mode, only streaming.

The `GET`/`REPORT` endpoints will return a 401 error if the `Authorization` header does not match an SDK key that is known to the Relay Proxy, just as the actual LaunchDarkly service endpoints would do for an invalid SDK key. They will return a 503 error if the Relay Proxy has not yet successfully obtained feature flag data from LaunchDarkly for the specified environment (either because it is still starting up, or because of a service outage or network interruption). In [automatic configuration mode](configuration.md#file-section-autoconfig), they will return a 503 error if the Relay Proxy has not yet received its configuration from LaunchDarkly.


### Endpoints that mobile SDKs use

All of these require an `Authorization` header whose value is the mobile key. 

| Endpoint                               |  Method  | Proxied Subdomain | Description                                                                           |
|----------------------------------------|:--------:|:-----------------:|---------------------------------------------------------------------------------------|
| `/meval/{contextBase64}`               |  `GET`   |  `clientstream.`  | SSE stream of "ping" and other events                                                 |
| `/meval`                               | `REPORT` |  `clientstream.`  | Same as above, but request body is the evaluation context JSON object (not in base64) |
| `/mobile`                              |  `POST`  |     `events.`     | For receiving events from mobile SDKs                                                 |
| `/mobile/events`                       |  `POST`  |     `events.`     | Same as above                                                                         |
| `/mobile/events/bulk`                  |  `POST`  |     `events.`     | Same as above                                                                         |
| `/mobile/events/diagnostic`            |  `POST`  |     `events.`     | Same as above                                                                         |
| `/mping`                               |  `GET`   |  `clientstream.`  | SSE stream for older SDKs that issues "ping" events when flags have changed           |
| `/msdk/evalx/contexts/{contextBase64}` |  `GET`   |   `clientsdk.`    | Polling endpoint, returns flag evaluation results for an evaluation context           |
| `/msdk/evalx/context`                  | `REPORT` |   `clientsdk.`    | Same as above but request body is the evaluation context JSON object (not in base64)  |
| `/msdk/evalx/users/{contextBase64}`    |  `GET`   |   `clientsdk.`    | Alternate name for `/msdk/evalx/contexts/{contextBase64}` used by older SDKs          |
| `/msdk/evalx/user`                     | `REPORT` |   `clientsdk.`    | Alternate name for `/msdk/evalx/context` used by older SDKs                           |

The `GET`/`REPORT` endpoints will return a 401 error if the `Authorization` header does not match an SDK key that is known to the Relay Proxy, just as the actual LaunchDarkly service endpoints would do for an invalid SDK key. They will return a 503 error if the Relay Proxy has not yet successfully obtained feature flag data from LaunchDarkly for the specified environment (either because it is still starting up, or because of a service outage or network interruption). In [automatic configuration mode](configuration.md#file-section-autoconfig), they will return a 503 error if the Relay Proxy has not yet received its configuration from LaunchDarkly.


### Endpoints that client-side JavaScript SDKs use

`{envId}` is the 32-hexdigit client-side environment ID (e.g. `6488674dc2ea1d6673731ba2`).

`{context}` is the base64 representation of an evaluation context JSON object. These endpoints accept both the current evaluation context JSON format (example: `{"kind": "user", "key": "abc", "attr1": true}`) and the older user JSON format (example: `{"key": "abc", "custom": {"attr1": true}}`).

These endpoints also support the `OPTION` method to enable CORS requests from browsers.

| Endpoint                                      |  Method  | Proxied Subdomain | Description                                                                          |
|-----------------------------------------------|:--------:|:-----------------:|--------------------------------------------------------------------------------------|
| `/a/{envId}.gif?d=*events*`                   |  `GET`   |     `events.`     | Alternative analytics event mechanism used if browser does not allow CORS            |
| `/eval/{envId}/{contextBase64}`               |  `GET`   |  `clientstream.`  | SSE stream of "ping" and other events for JS and other client-side SDK listeners     |
| `/eval/{envId}`                               | `REPORT` |  `clientstream.`  | Same as above but request body is the evaluation context JSON object (not in base64) |
| `/events/bulk/{envId}`                        |  `POST`  |     `events.`     | Receives analytics events from SDKs                                                  |
| `/events/diagnostic/{envId}`                  |  `POST`  |     `events.`     | Receives diagnostic data from SDKs                                                   |
| `/ping/{envId}`                               |  `GET`   |  `clientstream.`  | SSE stream for older SDKs that issues "ping" events when flags have changed          |
| `/sdk/evalx/{envId}/contexts/{contextBase64}` |  `GET`   |   `clientsdk.`    | Polling endpoint, returns flag evaluation results and additional metadata            |
| `/sdk/evalx/{envId}/contexts`                 | `REPORT` |   `clientsdk.`    | Same as above but request body is the evaluation context JSON object (not in base64) |
| `/sdk/evalx/{envId}/users/{contextBase64}`    |  `GET`   |   `clientsdk.`    | Alternate name for `/sdk/evalx/{envId}/contexts/{contextBase64}` used by older SDKs  |
| `/sdk/evalx/{envId}/users`                    | `REPORT` |   `clientsdk.`    | Alternate name for `/sdk/evalx/{envId}/contexts` used by older SDKs                  |
| `/sdk/goals/{envId}`                          |  `GET`   |   `clientsdk.`    | Provides goals data used by JS SDK                                                   |

The `GET`/`REPORT` endpoints return a 404 error if the environment ID is not recognized by Relay. This is different from the server-side and mobile endpoints, which return 401 for an unrecognized credential; it is consistent with the behavior of the corresponding LaunchDarkly service endpoints for client-side JavaScript SDKs.
