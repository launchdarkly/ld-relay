# LaunchDarkly Relay Proxy - Metrics

[(Back to README)](../README.md)

The Relay Proxy can export metrics via [OpenTelemetry Protocol (OTLP)](https://opentelemetry.io/docs/specs/otlp/) to any compatible backend, such as Prometheus, Datadog, Grafana, or an OpenTelemetry Collector. To learn about configuration, read [Configuration](./configuration.md).

## Available metrics

| Metric | Type | Unit | Description |
|--------|------|------|-------------|
| `http.server.active_requests` | UpDownCounter | `{request}` | The number of currently active stream connections from SDKs to the Relay Proxy. |
| `http.server.request.duration` | Histogram | `s` | The duration of requests to the Relay Proxy's service endpoints, in seconds. |
| `launchdarkly.relay.events.received.size` | Counter | `By` | The cumulative number of event bytes received by the Relay Proxy (measured after decompression). |
| `launchdarkly.relay.events.sent` | Counter | `{event}` | The cumulative number of events successfully sent to LaunchDarkly. |
| `launchdarkly.relay.events.sent.size` | Counter | `By` | The cumulative bytes of event payloads successfully sent to LaunchDarkly. |
| `launchdarkly.relay.events.send.errors` | Counter | `{event}` | The cumulative number of events that failed to send after all retries. |
| `launchdarkly.relay.events.dropped` | Counter | `{event}` | The cumulative number of events dropped due to capacity overflow. |
| `launchdarkly.relay.events.pending` | Gauge | `{event}` | The current number of events buffered in the queue. |

## Attributes

All metrics include the following attributes:

| Attribute | Description |
|-----------|-------------|
| `relay.id` | A unique identifier for this Relay Proxy instance, generated at startup. |
| `environment.name` | The name of the LaunchDarkly environment as configured in the Relay Proxy. In automatic configuration or offline mode, this is the actual project and environment name from LaunchDarkly. Example: `MyApplication Staging` |
| `platform.category` | The kind of SDK that generated the metric: `server`, `mobile`, or `browser`. |
| `user_agent` | The user agent of the SDK making the request. Example: `Node/3.4.0` |
| `sdk.wrapper` | The SDK wrapper identifier, if provided. Example: `flutter-client/2.0.0` |
| `http.route` | The request URL path template. Variables appear as placeholders rather than actual values. Example: `/sdk/evalx/{envId}/contexts/{context}` |
| `http.request.method` | The HTTP method. Example: `GET` |
| `url.scheme` | The URL scheme. Example: `https` |
| `application.id` | The application identifier, extracted from the `application-id` field of the `X-LaunchDarkly-Tags` header. |
| `application.version` | The application version, extracted from the `application-version` field of the `X-LaunchDarkly-Tags` header. |
| `instance.id` | The SDK instance identifier from the `X-LaunchDarkly-Instance-Id` header. |

## Backend-specific notes

### Prometheus

Prometheus supports OTLP ingestion natively since v2.47.0. Enable it with `--web.enable-otlp-receiver` and configure the Relay Proxy to push metrics to Prometheus's OTLP endpoint:

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://<prometheus-host>:9090/api/v1/otlp/v1/metrics
OTEL_EXPORTER_OTLP_PROTOCOL=http
```

Prometheus converts OpenTelemetry metric names by replacing dots with underscores, so the metrics will appear as `http_server_active_requests`, `http_server_request_duration_seconds`, `launchdarkly_relay_events_received_size_total`, etc.

### OpenTelemetry Collector

For more complex setups — such as routing metrics to multiple backends simultaneously — point the Relay Proxy at an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/):

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector-host>:4317
```

The collector can then forward metrics to any supported backend.
