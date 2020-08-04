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
    "name of first environment": {
      "sdkKey": "sdk-********-****-****-****-*******99999",
      "envId": "999999999999999999999999",
      "mobileKey": "mob-********-****-****-****-*******99999",
      "status": "connected"
    },
    "name of another environment": {
      "sdkKey": "sdk-********-****-****-****-*******99999",
      "envId": "999999999999999999999999",
      "mobileKey": "mob-********-****-****-****-*******99999",
      "status": "connected"
    }
  },
  "status": "healthy",
  "version": "5.11.1",
  "clientVersion": "4.17.2"
}
```

The `status` property for each environment is `"connected"` if the Relay Proxy was able to establish a LaunchDarkly connection and get feature flag data for that environment, or `"disconnected"` if not. This does not take into account any service outages that happened after the connection was initially made.

The top-level `status` property will be `"healthy"` if all of the environments are `"connected"`, or `"degraded"` if any of the environments is `"disconnected"`.

The `version` property is the version of the Relay Proxy; `clientVersion` is the version of the Go SDK that the Relay Proxy is using.

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
`/sdk/flags/{flagKey}`       | `GET`  | `app.`    | Polling endpoint for [PHP SDK](./php.md)
`/sdk/segments/{segmentKey}` | `GET`  | `app.`    | Polling endpoint for [PHP SDK](./php.md)

For server-side SDKs other than PHP, the Relay Proxy does not support polling mode, only streaming.


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


### Endpoints that client-side JavaScript SDKs use

`{clientId}` is the 32-hexdigit client-side environment ID (e.g. `6488674dc2ea1d6673731ba2`).

`{user}` is the base64 representation of a user JSON object (e.g. `{"key": "user1"}` => `eyJrZXkiOiAidXNlcjEifQ==`).

These endpoints also support the `OPTION` method to enable CORS requests from browsers.

Endpoint                             | Method   | Proxied Subdomain | Description
-------------------------------------|:--------:|:---------------:|------------------------------------
`/a/{clientId}.gif?d=*events*`       | `GET`    | `events.`       | Alternative analytics event mechanism used if browser does not allow CORS
`/eval/{clientId}/{user}`            | `GET`    | `clientstream.` | SSE stream of "ping" and other events for JS and other client-side SDK listeners
`/eval/{clientId}`                   | `REPORT` | `clientstream.` | Same as above but request body is user JSON object
`/events/bulk/{clientId}`            | `POST`   | `events.`       | Receives analytics events from SDKs
`/events/diagnostic/{clientId}`      | `POST`   | `events.`       | Receives diagnostic data from SDKs
`/ping/{clientId}`                   | `GET`    | `clientstream.` | SSE stream for older SDKs that issues "ping" events when flags have changed
`/sdk/eval/{clientId}/users/{user}`  | `GET`    | `app.`          | Polling endpoint for older SDKs, returns flag evaluation results for a user
`/sdk/eval/{clientId}/users`         | `REPORT` | `app.`          | Same as above but request body is user JSON object
`/sdk/evalx/{clientId}/users/{user}` | `GET`    | `app.`          | Polling endpoint, returns flag evaluation results and additional metadata
`/sdk/evalx/{clientId}/users`        | `REPORT` | `app.`          | Same as above but request body is user JSON object
`/sdk/goals/{clientId}`              | `GET`    | `app.`          | Provides goals data used by JS SDK
