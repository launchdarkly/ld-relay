# LaunchDarkly Relay Proxy - Metrics

[(Back to README)](../README.md)

The Relay Proxy can export metrics via [OpenTelemetry Protocol (OTLP)](https://opentelemetry.io/docs/specs/otlp/) to any compatible backend, such as Prometheus, Datadog, Grafana, or an OpenTelemetry Collector. To learn about configuration, read [Configuration](./configuration.md).

## Available metrics

| Metric | Type | Description |
|--------|------|-------------|
| `launchdarkly.relay.connections` | UpDownCounter | The number of currently active stream connections from SDKs to the Relay Proxy. |
| `launchdarkly.relay.requests` | Counter | The cumulative number of requests received by the Relay Proxy's [service endpoints](./endpoints.md) since startup. |
| `launchdarkly.relay.request.duration` | Histogram | The duration of requests to the Relay Proxy's service endpoints, in seconds. |
| `launchdarkly.relay.events.received.bytes` | Counter | The cumulative number of event bytes received by the Relay Proxy (measured after decompression). |

## Attributes

All metrics include the following attributes:

| Attribute | Description |
|-----------|-------------|
| `relayId` | A unique identifier for this Relay Proxy instance, generated at startup. |
| `env` | The name of the LaunchDarkly environment as configured in the Relay Proxy. In automatic configuration or offline mode, this is the actual project and environment name from LaunchDarkly. Example: `MyApplication Staging` |
| `platformCategory` | The kind of SDK that generated the metric: `server`, `mobile`, or `browser`. |
| `userAgent` | The user agent of the SDK making the request. Example: `Node/3.4.0` |
| `sdkWrapper` | The SDK wrapper identifier, if provided. Example: `flutter-client/2.0.0` |
| `route` | The request URL path template. Variables appear as placeholders rather than actual values. Example: `/sdk/evalx/{envId}/contexts/{context}` |
| `method` | The HTTP method. Example: `GET` |
| `application.id` | The application identifier, extracted from the `application-id` field of the `X-LaunchDarkly-Tags` header. |
| `application.version` | The application version, extracted from the `application-version` field of the `X-LaunchDarkly-Tags` header. |
| `instanceId` | The SDK instance identifier from the `X-LaunchDarkly-Instance-Id` header. |

## Backend-specific notes

### Prometheus

Prometheus supports OTLP ingestion natively since v2.47.0. Enable it with `--web.enable-otlp-receiver` and configure the Relay Proxy to push metrics to Prometheus's OTLP endpoint:

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://<prometheus-host>:9090/api/v1/otlp/v1/metrics
OTEL_EXPORTER_OTLP_PROTOCOL=http
```

Prometheus converts OpenTelemetry metric names by replacing dots with underscores, so the metrics will appear as `launchdarkly_relay_connections`, `launchdarkly_relay_requests_total`, `launchdarkly_relay_request_duration_seconds`, and `launchdarkly_relay_events_received_bytes_total`.

### OpenTelemetry Collector

For more complex setups — such as routing metrics to multiple backends simultaneously — point the Relay Proxy at an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/):

```
USE_OTLP=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://<collector-host>:4317
```

The collector can then forward metrics to any supported backend.
