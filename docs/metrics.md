# LaunchDarkly Relay Proxy - Metrics integrations

[(Back to README)](../README.md)

You can configure the Relay Proxy to export statistics and route traces to Datadog, Stackdriver, and Prometheus. To learn about the available settings for each of these options, read [Configuration](./docs/configuration.md).

The Relay Proxy suppors the following metrics:

- `connections`: The number of current proxied streaming connections.
- `newconnections`: The number of streaming connections created.
- `requests`: Number of requests received.

You can filter metrics by the following tags:

- `platformCategoryTagKey`: The platform a metric was generated by (e.g. server, browser, or client-side).
- `env`: The name of the LaunchDarkly environment.
- `route`: The request route.
- `method`: The http method used for the request.
- `userAgent`: The user agent used to make the request, typically a LaunchDarkly SDK version. Example: "Node/3.4.0"

**Note:** Traces for stream connections will trace until the connection is closed.