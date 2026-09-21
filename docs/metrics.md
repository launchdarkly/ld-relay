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

### Initialization-delivery limiter metrics

These instruments are active only when the `[Concurrency]` limit is configured; read
[Configuration](./configuration.md). They come from the relay's own layers, so one limitation
applies: a `connection_ended` delivery outcome does not say why the connection ended -- a client
disconnect and a relay deadline cut look the same.

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `launchdarkly.relay.init.slots.held` | Gauge | `{slot}` | The number of initialization-delivery slots currently held. |
| `launchdarkly.relay.init.queue.waiting` | Gauge | `{request}` | The number of requests currently waiting for a slot. |
| `launchdarkly.relay.init.admitted` | Counter | `{request}` | The cumulative number of requests the budget admitted. |
| `launchdarkly.relay.init.rejected` | Counter | `{request}` | The cumulative number of requests the budget rejected, with the cause in `launchdarkly.relay.init.reason`: `budget_full`, `client_gone`, or `shutdown`. Only `budget_full` shows saturation; alert on that cause, not on the total. |
| `launchdarkly.relay.init.deliveries` | Counter | `{delivery}` | Gated stream deliveries, with `launchdarkly.relay.init.protocol` (`fdv1` or `fdv2`), `launchdarkly.relay.init.outcome` (`completed`, `connection_ended`, or `read_error`), and `launchdarkly.relay.init.cap_engaged` (the absolute cap, not the throughput floor, governed the delivery). |
| `launchdarkly.relay.init.sheds` | Counter | `{request}` | Requests shed because the budget and queue were full, with `launchdarkly.relay.init.transport` (`poll` or `stream`). Poll sheds carry `launchdarkly.environment.name`; stream sheds do not, because the stream layer does not know its environment. |
| `launchdarkly.relay.init.replays.up_to_date` | Counter | `{reply}` | Stream replays answered with the small up-to-date reply, which is never charged to the budget. `launchdarkly.relay.init.after_wait` is true when the client's basis became current while it waited in the queue. |
| `launchdarkly.relay.init.deadline.set_errors` | Counter | `{error}` | Failed attempts to set the connection write deadline. Above zero, the deadline protection is not in force. |

### Status metrics

These report what the [status endpoint](./endpoints.md) reports, read from the same code that
serves it, so a metric cannot describe a state the document would not.

Every state field carries the state in `launchdarkly.relay.state`. The relay-level metrics below
are exported whenever OTLP export is enabled, at a fixed cardinality of about twenty series
whatever the environment count.

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `launchdarkly.relay.status` | Gauge | `{state}` | Whether Relay reports itself `healthy` or `degraded`. |
| `launchdarkly.relay.environment.count` | Gauge | `{environment}` | The number of environments Relay serves. |
| `launchdarkly.relay.environment.status.count` | Gauge | `{environment}` | Environments that are `connected` or `disconnected`. |
| `launchdarkly.relay.environment.connection.state.count` | Gauge | `{environment}` | Environments whose data source is in each state: `VALID`, `INITIALIZING`, `INTERRUPTED`, `OFF`. |
| `launchdarkly.relay.environment.datastore.state.count` | Gauge | `{environment}` | Environments whose data store is in each state: `VALID`, `INITIALIZING`, `INTERRUPTED`. |
| `launchdarkly.relay.environment.expiring_key.count` | Gauge | `{environment}` | Environments still serving an expiring SDK key. |
| `launchdarkly.relay.environment.big_segments.unavailable.count` | Gauge | `{environment}` | Environments whose big segment store could not be read. |
| `launchdarkly.relay.environment.big_segments.stale.count` | Gauge | `{environment}` | Environments whose big segment data is past `bigSegmentsStaleThreshold`. |

In automatic configuration mode, three more describe the configuration stream. A state other than
`VALID` means Relay has stopped learning about added, deleted, or re-keyed environments while the
ones it knows keep serving flags -- a failure nothing else here reveals. Absent in other modes.

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `launchdarkly.relay.autoconfig.state` | Gauge | `{state}` | The state of the auto-configuration stream. |
| `launchdarkly.relay.autoconfig.state.since` | Gauge | `s` | When the stream entered its current state, in Unix seconds. |
| `launchdarkly.relay.autoconfig.last_error` | Gauge | `s` | When the stream last failed, in Unix seconds, with `launchdarkly.relay.error.kind` and `http.response.status_code`. Absent until it fails. |

#### Per-environment status metrics

These name *which* environment the counts above counted. They are **off by default**; turn them on
with `environmentStatusMetrics` / `OTEL_ENVIRONMENT_STATUS_METRICS`. All of them carry
`launchdarkly.environment.name`, the same value the request metrics use, so status and traffic
series join.

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `launchdarkly.relay.environment.status` | Gauge | `{state}` | Whether the environment is `connected` or `disconnected`. |
| `launchdarkly.relay.environment.connection.state` | Gauge | `{state}` | The state of the environment's data source. |
| `launchdarkly.relay.environment.connection.state.since` | Gauge | `s` | When the data source entered its current state, in Unix seconds. |
| `launchdarkly.relay.environment.connection.last_error` | Gauge | `s` | When the data source last failed, in Unix seconds, with `launchdarkly.relay.error.kind` and `http.response.status_code`. Absent until it fails. |
| `launchdarkly.relay.environment.datastore.state` | Gauge | `{state}` | The state of the environment's data store. |
| `launchdarkly.relay.environment.datastore.state.since` | Gauge | `s` | When the data store entered its current state, in Unix seconds. |
| `launchdarkly.relay.environment.big_segments.available` | Gauge | `{state}` | Whether the big segment store could be read. Absent without a big segment store. |
| `launchdarkly.relay.environment.big_segments.stale` | Gauge | `{state}` | Whether the big segment data is past the staleness threshold. |
| `launchdarkly.relay.environment.big_segments.last_synchronized` | Gauge | `s` | When the big segment data last synchronized, in Unix seconds. Absent until it does. |
| `launchdarkly.relay.environment.expiring_key` | Gauge | `{state}` | Whether the environment still serves an expiring SDK key. |
| `launchdarkly.relay.environment.info` | Gauge | `{environment}` | Always 1. Carries `launchdarkly.environment.id`, `launchdarkly.environment.key`, `launchdarkly.project.key`, and `launchdarkly.project.name`. |
| `launchdarkly.relay.environment.datastore.info` | Gauge | `{environment}` | Always 1. Carries `db.system.name`, `server.address`, `launchdarkly.relay.store.prefix`, and `db.collection.name`. Absent for an in-memory store. |

SDK and mobile keys are never exported. Identity lives on the two `info` series rather than on
every state series, which is also how a query reaches the environment ID: these metrics report the
display name, while `/status` is keyed by ID in automatic configuration mode.

The display name is the configured name, or `<project name> <environment name>` where there is
none. Project names are not unique, so two environments can produce one display name -- their
series merge, reporting whichever Relay collected last, while the `info` series stay separate and
the environment counts stay correct. `/status` remains unambiguous.

#### Reading a state

Each state field reports one series per possible state. The current state reads 1 and the others
read 0 rather than being omitted, so a transition cannot leave two states at 1 while your backend
still holds the previous sample.

```
# is this Relay degraded?
launchdarkly_relay_status{launchdarkly_relay_state="degraded"} == 1

# how many environments are not receiving updates, anywhere in the fleet?
sum(launchdarkly_relay_environment_connection_state_count{launchdarkly_relay_state="INTERRUPTED"})

# which environments are interrupted (needs the per-environment metrics)
launchdarkly_relay_environment_connection_state{launchdarkly_relay_state="INTERRUPTED"} == 1
```

Alert on a state that persists with the rule's own `for` clause:

```yaml
- alert: RelayEnvironmentInterrupted
  expr: launchdarkly_relay_environment_connection_state{launchdarkly_relay_state="INTERRUPTED"} == 1
  for: 5m
```

The `state.since` metrics answer what `for` cannot: how long the current state has held, as
`time() - launchdarkly_relay_autoconfig_state_since_seconds`. They are timestamps, not ages, so the
value changes only when the state does.

To chart a state *changing*, collapse the 0/1 series into one whose value identifies the state:
multiply each state by a number, combine with `or`, and map the numbers back to names in the panel.
`max by` drops the state label so the operands become one series -- add
`launchdarkly_environment_name` for a per-environment field.

```
  (max by (instance) (launchdarkly_relay_autoconfig_state{launchdarkly_relay_state="VALID"}        == 1) * 0)
or (max by (instance) (launchdarkly_relay_autoconfig_state{launchdarkly_relay_state="INITIALIZING"} == 1) * 1)
or (max by (instance) (launchdarkly_relay_autoconfig_state{launchdarkly_relay_state="INTERRUPTED"}  == 1) * 2)
or (max by (instance) (launchdarkly_relay_autoconfig_state{launchdarkly_relay_state="OFF"}          == 1) * 3)
```

#### Cardinality and cost

- The per-environment metrics are off by default because their cardinality follows the environment
  count, not the traffic: about 18 series per environment Relay knows about, idle ones included,
  which in automatic configuration mode is every environment in the account. Past the
  [cardinality limit](#cardinality-limit) the loss is silent, so raise it first.
- Status is read once per collection cycle (`OTEL_METRIC_EXPORT_INTERVAL`, 60 seconds by default),
  so a state change that begins and ends between two collections is never sampled. A moved
  `state.since` is the only evidence it happened.
- Every collection reads each big segment store, because those counts are relay-level. It is the
  query `/status` already runs per request, now also on a timer.

#### What these metrics do not tell you

- **Whether the store is alive.** The data store state changes only when Relay writes and the write
  fails, and an idle Relay never writes, so a dead store reads `VALID` until the next flag update.
  `big_segments.unavailable` is the exception: it reads the store every collection.
- **Whether a Relay is running.** A stopped process does not read zero. Its series stop being
  written and Prometheus answers with the last sample for its lookback, five minutes by default, so
  a dashboard draws it as healthy for that long. Pushed metrics have no `up` series; the age of the
  data is the signal:

  ```
  time() - timestamp(launchdarkly_relay_status{launchdarkly_relay_state="healthy"}) > 120
  ```
- **Which failure is current**, when there have been several. A `last_error` series carries the kind
  and status code as attributes, so a new kind starts a new series while the old one answers until
  the lookback expires. Take the latest timestamp, or wrap the query in `last_over_time`.

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
| `service.version` | The Relay Proxy version, the same value the status endpoint reports as `version`. Overridable with `service.version` in `OTEL_RESOURCE_ATTRIBUTES`. Example: `9.0.0` |
| `launchdarkly.relay.sdk.version` | The version of the Go SDK this Relay Proxy embeds, the same value the status endpoint reports as `clientVersion`. Example: `7.17.0` |

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

Values that are present are reported as received, apart from bytes that are not valid UTF-8, which are
stripped because they would otherwise fail the OTLP export. In particular a value containing a slash --
an application version such as `2026/08/01`, or an environment name such as `My Project / Staging` --
keeps it.

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
OTEL_EXPORTER_OTLP_ENDPOINT=http://<prometheus-host>:9090/api/v1/otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http
```

`OTEL_EXPORTER_OTLP_ENDPOINT` is a base URL -- the SDK appends `/v1/metrics` -- so it is the root
of the OTLP receiver, not the full path. The signal-specific `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`
is used as given, and there `http://<prometheus-host>:9090/api/v1/otlp/v1/metrics` is correct.

The Relay Proxy sends traces and logs to the same endpoint, which Prometheus answers with 404. To
keep those out of the way, point everything at an
[OpenTelemetry Collector](#opentelemetry-collector) and let it forward only the metrics.

Prometheus renames metrics as it ingests them: dots become underscores, counters gain `_total`, and
anything measured in seconds gains `_seconds`. So the instruments above arrive as
`http_server_active_requests`, `launchdarkly_relay_requests_total`, and
`launchdarkly_relay_environment_connection_state_since_seconds`. The state gauges gain no suffix.

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
