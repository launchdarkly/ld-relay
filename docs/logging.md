# LaunchDarkly Relay Proxy - Logging

[(Back to README)](../README.md)

Like the Go SDK, the Relay Proxy supports four logging levels: 

* Debug (most verbose), 
* Info (default), 
* Warn, and 
* Error. 

If you set the logging level to Warn, Info and Debug are disabled. Similarly, if you set the logging level to Info, Debug is disabled. 

There are two categories of log output: global messages and per-environment messages.

## Global logging

Global messages are from the general Relay Proxy infrastructure. For example, logging when the Relay Proxy has successfully started up.

The minimum enabled log level for global messages is controlled in [your configuration](./configuration.md#file-section-man) by the `[Main] logLevel` parameter, or the `LOG_LEVEL` environment variable. The default is Info.

## Per-environment logging

Per-environment messages are for the Relay Proxy's interaction with LaunchDarkly for a specific one of your configured environments. For example, receiving a flag update or sending analytics events. 

These messages mostly come from the LaunchDarkly Go SDK, which the Relay Proxy uses to communicate with LaunchDarkly.

The minimum enabled log level for per-environment messages is controlled in [your configuration](./configuration.md#file-section-environment-name) by the `[Environment "envname"] logLevel` parameter, or the `LD_LOG_LEVEL_envname` environment variable. You can set this separately for each environment. 

If you don't specify a log level for an environment, it uses the same log level that was specified for global messages. You can control these messages separately in the [configuration](./configuration.md). For instance, you may wish to see more verbose output in one environment than another, or enable Debug logging globally for HTTP requests without enabling it for per-environment messages.

## Log format

The Relay Proxy supports two log output formats:

* **Text** (default): Traditional text-based log format with timestamps
* **JSON**: JSON-structured log output for easier parsing by log aggregation systems

The log format is controlled by the `LOG_FORMAT` environment variable. Valid values are `text` (default) and `json`. This must be set as an environment variable (not in the configuration file) so that all log output, including startup messages, uses the correct format.

### Text format example

```
2024/01/15 10:30:45.123456 INFO: Starting LaunchDarkly relay version 8.0.0
```

### JSON format example

```json
{"timestamp":"2024-01-15T10:30:45.123456789Z","level":"INFO","message":"Starting LaunchDarkly relay version 8.0.0"}
```

Each JSON log entry contains:
- `timestamp`: RFC3339Nano formatted UTC timestamp
- `level`: Log level (DEBUG, INFO, WARN, or ERROR)
- `message`: The log message content

## OTLP log export

When OTLP is enabled with `USE_OTLP=true`, the Relay Proxy exports log records over OTLP in addition
to writing them to the console, using the same endpoint and protocol as metrics and traces. To learn
about configuration, read [Configuration](./configuration.md#file-section-opentelemetry).

Console output is never affected by this: log records always go to stdout and stderr, and the export
is an additional copy for a collector.

To leave metrics and traces exporting but turn log export off, set `OTEL_LOGS_EXPORTER=none`.

Two situations call for that:

* **The collector does not accept OTLP logs.** Some collectors accept traces and metrics out of the
  box but have log ingestion disabled by default, in which case every export attempt fails. The
  Datadog Agent behaves this way; to learn more, read [Metrics](./metrics.md#datadog).
* **Something already collects the console output.** On a host or container deployment where an agent
  tails stdout or the systemd journal, exporting over OTLP as well delivers every record twice.

## Debug logging

Enabling the Debug log level for global messages causes the Relay Proxy to log every HTTP request that it receives.

For per-environment messages, Debug logging includes verbose information about the operation of the Go SDK, which this may include user properties and feature flag keys. You will normally not want to enable this output, so if you have set the global level to Debug to log HTTP requests, you should set it to something other than Debug for your environments.
