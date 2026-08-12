# LaunchDarkly Relay Proxy - Metrics

[(Back to README)](../README.md)

The Relay Proxy can export metrics via [OpenTelemetry Protocol (OTLP)](https://opentelemetry.io/docs/specs/otlp/) to any compatible backend, such as Prometheus, Datadog, Grafana, or an OpenTelemetry Collector. To learn about configuration, read [Configuration](./configuration.md). The same setting also exports traces; to learn about those, read [Tracing](./tracing.md).

## Available metrics

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `http.server.active_requests` | UpDownCounter | `{request}` | The number of requests currently in flight, across every endpoint the Relay Proxy serves. Use the `launchdarkly.relay.endpoint.type` attribute to narrow this to a single kind of endpoint -- for example, filtering to `stream` gives the number of open SSE connections from SDKs. |
| `launchdarkly.relay.requests` | Counter | `{request}` | The cumulative number of requests the Relay Proxy has received since startup, across every endpoint it serves. Counted when the request starts, so a streaming request is counted when the connection is made rather than when it closes. |
| `http.server.request.duration` | Histogram | `s` | The duration of requests to the Relay Proxy's service endpoints, in seconds. This is not recorded for streaming responses, whose lifetime is unbounded. |
| `launchdarkly.relay.events.received.size` | Counter | `By` | The cumulative number of event bytes received by the Relay Proxy (measured after decompression). |
| `launchdarkly.relay.events.sent` | Counter | `{event}` | The cumulative number of events successfully sent to LaunchDarkly. |
| `launchdarkly.relay.events.sent.size` | Counter | `By` | The cumulative bytes of event payloads successfully sent to LaunchDarkly. |
| `launchdarkly.relay.events.failed` | Counter | `{event}` | The cumulative number of events that could not be delivered after all retries. |
| `launchdarkly.relay.events.dropped` | Counter | `{event}` | The cumulative number of events dropped due to capacity overflow. |
| `launchdarkly.relay.events.pending` | Gauge | `{event}` | The current number of events buffered in the queue. |

### Counting stream connections

Version 8 exported two stream-specific metrics, `connections` and `newconnections`. Neither has a
metric of its own now, because active requests and total requests are counted for every endpoint and
the `launchdarkly.relay.endpoint.type` attribute says which kind of endpoint served the request.
Filter that attribute to `stream` to get the same numbers:

| Version 8 metric | Equivalent |
|---|---|
| `connections` | `http.server.active_requests` where `launchdarkly.relay.endpoint.type="stream"` |
| `newconnections` | `launchdarkly.relay.requests` where `launchdarkly.relay.endpoint.type="stream"` |

In Prometheus, the rate at which SDKs open stream connections is:

```
sum(rate(launchdarkly_relay_requests_total{launchdarkly_relay_endpoint_type="stream"}[5m]))
```

Version 8 reported these per SDK kind through a `platformCategory` tag. That attribute is no longer
reported, so break the same query down by `http_route` instead -- the server-side, mobile, and
client-side stream endpoints are separate routes.

## Resource attributes

These describe the Relay Proxy process itself and are attached to every metric and span, rather than
being repeated on each measurement:

| Attribute | Description |
|-----------|-------------|
| `service.name` | `ld-relay`, unless overridden with `OTEL_SERVICE_NAME` or with `service.name` in `OTEL_RESOURCE_ATTRIBUTES`. `OTEL_SERVICE_NAME` wins between the two. |
| `service.instance.id` | A unique identifier for this Relay Proxy process, generated at startup, unless you supply your own via `OTEL_RESOURCE_ATTRIBUTES`. Prometheus exposes it as the `instance` label. Example: `5f313039-df4e-45f5-ad9e-4afd840cb210` |

Note that resource attributes are **not** copied onto every series. Prometheus reports them through
`target_info`, so a query that needs the process identity has to join against it -- or use the
`instance` label, which carries the same value.

If you supply your own `service.instance.id`, give each process a distinct value. The attribute is what
tells one Relay Proxy apart from another, so a value shared across replicas -- a literal in a ConfigMap,
for instance -- merges their series and their `target_info`, and the `instance` label no longer
identifies a process. Note also that the identifier Relay generates is the same one it reports to
LaunchDarkly with its usage data; overriding the attribute changes what your telemetry backend sees, not
what LaunchDarkly sees, so the two no longer match.

## Request attributes

The request metrics -- `http.server.active_requests`, `launchdarkly.relay.requests`,
`http.server.request.duration`, and `launchdarkly.relay.events.received.size` -- include the
following:

| Attribute | Description |
|-----------|-------------|
| `launchdarkly.environment.name` | The name of the LaunchDarkly environment as configured in the Relay Proxy. In automatic configuration or offline mode, this is the actual project and environment name from LaunchDarkly. Example: `MyApplication Staging` |
| `user_agent.original` | The `User-Agent` header sent by the SDK making the request, as received. Example: `Node/3.4.0` |
| `http.route` | The request URL path template. Variables appear as placeholders rather than actual values. Example: `/sdk/evalx/{envId}/contexts/{context}` |
| `http.request.method` | The HTTP method. Example: `GET` |
| `url.scheme` | The URL scheme. Example: `https` |
| `launchdarkly.application.id` | The application identifier, extracted from the `application-id` field of the `X-LaunchDarkly-Tags` header. |
| `launchdarkly.application.version` | The application version, extracted from the `application-version` field of the `X-LaunchDarkly-Tags` header. |
| `launchdarkly.relay.endpoint.type` | The kind of endpoint that served the request: `stream`, `poll`, `events`, `goals`, or `status`. Requests that matched no route report `not_provided`. |

`http.server.active_requests` and `launchdarkly.relay.requests` carry exactly the attributes above, so
the two can be joined. Neither can report anything that is only known once the handler has finished,
because both are recorded when the request starts: `http.server.active_requests` would leak a
permanently non-zero series if its increment and decrement disagreed on the attributes.
`http.server.request.duration` is recorded at the end of the request, so it additionally carries
`http.response.status_code`, `network.protocol.version`, and -- for a 5xx response -- `error.type`.

Note that `launchdarkly.environment.name` is a *LaunchDarkly* environment, which has nothing to do
with the OpenTelemetry `deployment.environment.name` attribute described under
[Datadog](#datadog) below. The two are unrelated, and both can be set at once.

The event delivery metrics (`launchdarkly.relay.events.sent`, `.sent.size`, `.failed`,
`.dropped`, `.pending`) are recorded outside any request, so they carry only
`launchdarkly.environment.name`.

Every measurement on `.failed` is a failure, so it always carries `error.type`. When the events
service returned a response, `error.type` is that status code as a string and
`http.response.status_code` carries it as a number. When the send failed before any response arrived
-- a network error or a timeout -- `error.type` is `_OTHER` and no status code is reported.

Attribute values that are absent are reported as `not_provided` rather than being omitted. The status
endpoints and requests that matched no route are not associated with an SDK or an LD environment, so
they report `not_provided` for `launchdarkly.environment.name` and the other SDK attributes.

`platform.category`, `sdk.wrapper`, and `instance.id` are no longer reported on metrics. `instance.id`
in particular is per SDK *instance*, which made these metrics grow a series per client process. All
three are still included in the usage data the Relay Proxy sends to LaunchDarkly.

## Cardinality limit

The OpenTelemetry SDK caps how many distinct attribute sets a single instrument can record in one
export cycle. The default cap is 2000. Once an instrument reaches it, any attribute set it has not
already recorded in that cycle is folded into one series carrying only `otel.metric.overflow=true`;
attribute sets already being recorded continue normally. Nothing is logged when this happens, so an
`otel.metric.overflow` series showing up in your backend is the signal that the cap has been reached
and that some series are being merged.

Because the request attributes above multiply together -- environments times user agents times routes
times status codes, and so on -- a Relay Proxy serving many environments or a diverse SDK fleet can
reach the cap. Raise it, or remove it entirely, with `metricsCardinalityLimit` /
`OTEL_METRICS_CARDINALITY_LIMIT`:

```
OTEL_METRICS_CARDINALITY_LIMIT=20000   # raise the cap
OTEL_METRICS_CARDINALITY_LIMIT=0       # no cap
```

The limit applies to every instrument, including the Go runtime metrics. Removing it entirely means
memory use and the size of each export grow with however many attribute combinations your traffic
produces, so prefer raising it to a value your backend can absorb.

## Backend-specific notes

### Prometheus

Prometheus supports OTLP ingestion natively since v2.47.0. Enable it with `--web.enable-otlp-receiver` and configure the Relay Proxy to push metrics to Prometheus's OTLP endpoint:

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://<prometheus-host>:9090/api/v1/otlp/v1/metrics
OTEL_EXPORTER_OTLP_PROTOCOL=http
```

Prometheus converts OpenTelemetry metric names by replacing dots with underscores, and appends `_total` to counters, so the metrics will appear as `http_server_active_requests`, `launchdarkly_relay_requests_total`, `http_server_request_duration_seconds`, `launchdarkly_relay_events_received_size_total`, etc.

### Datadog

The [Datadog Agent](https://docs.datadoghq.com/opentelemetry/setup/otlp_ingest_in_the_agent/) can accept OTLP metrics directly, but OTLP ingestion must be enabled in the Agent's `datadog.yaml`:

```yaml
otlp_config:
  receiver:
    protocols:
      grpc:
        endpoint: "0.0.0.0:4317"
```

Then configure the Relay Proxy:

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta
```

**Important:** Datadog requires delta aggregation temporality. You must set `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta` or Datadog may discard data points. The OpenTelemetry SDK defaults to cumulative temporality.

The `service.name` resource attribute (set via `OTEL_SERVICE_NAME`) maps to Datadog's `service` tag. You can also set `deployment.environment.name` and `service.version` via `OTEL_RESOURCE_ATTRIBUTES` to populate Datadog's unified service tags:

```
OTEL_SERVICE_NAME=ld-relay
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=production,service.version=9.0.0
```

### OpenTelemetry Collector

For more complex setups — such as routing metrics to multiple backends simultaneously — point the Relay Proxy at an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/):

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector-host>:4317
```

The collector can then forward metrics to any supported backend.
