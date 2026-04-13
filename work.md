# Logging Infrastructure Migration: ldlog -> log/slog

## Motivation

The ld-relay project used a custom logging setup built on `ldlog.Loggers` from
`go-sdk-common/v3`. Plain-text and JSON logging were shoe-horned in via a custom
`JSONLogger` that parses level prefixes back out of formatted strings. This was
neither idiomatic nor extensible.

The replacement uses `log/slog` (Go stdlib) which provides:

- Idiomatic structured logging (stdlib since Go 1.21; project is on Go 1.24)
- Built-in `TextHandler` / `JSONHandler` for format switching
- Official OTel bridge (`otelslog`) for log export
- Dynamic level control via `slog.LevelVar`

## New Files

| File | Purpose |
|------|---------|
| `internal/logging/logger.go` | `NewLogger()` factory with functional options for format, level, and OTel handler |
| `internal/logging/handler.go` | `StderrSplitHandler` (error -> stderr, rest -> stdout) and `MultiHandler` (fan-out to multiple handlers) |
| `internal/logging/bridge.go` | `NewLDLogBridge()` adapts `*slog.Logger` to `ldlog.Loggers` for the LD SDK |
| `internal/logging/level.go` | `LevelNone` constant and conversion functions between `slog.Level` and `ldlog.LogLevel` |
| `internal/logging/otel.go` | `NewOTelLogProvider()` creates an OTLP log exporter via the `otelslog` bridge |
| `internal/logging/logtest/mock_handler.go` | `MockHandler` for capturing and asserting on structured log records in tests |

## Deleted Files

| File | Reason |
|------|--------|
| `internal/logging/json_logger.go` | Replaced by `slog.JSONHandler` |
| `internal/logging/json_logger_test.go` | Tests for deleted file |
| `internal/logging/default_loggers.go` | Replaced by `logger.go` / `NewLogger()` |
| `internal/logging/default_loggers_test.go` | Tests for deleted file |

## Architectural Changes

### Logger type

Every struct field, function parameter, and interface method that previously used
`ldlog.Loggers` now uses `*slog.Logger`. This includes:

- `Relay.logger`
- `EnvContext.GetLogger()`
- `EnvContextImplParams.Logger`
- `NewRelay()`, `NewManager()`, `NewHTTPConfig()`, `NewEnvStreams()`, etc.

### Per-environment logging

Previously each environment copied the root `ldlog.Loggers` and called
`SetPrefix()` / `SetMinLevel()` on it. Now environments use:

```go
envLogger := params.Logger.With("env", logPrefix)
```

This adds a structured `env` attribute to every log record from that
environment, which is cleaner than string-prefix concatenation and works
correctly with both text and JSON output.

### SDK bridge

The LaunchDarkly Go SDK requires `ldlog.Loggers` (a struct, not an interface),
so we cannot eliminate the `ldlog` dependency entirely. Instead,
`logging.NewLDLogBridge(logger)` creates an `ldlog.Loggers` instance whose
`BaseLogger` delegates to the given `*slog.Logger`. The bridge sets ldlog's
minimum level to `Debug` so that slog's handler controls all filtering.

The `ldlog` import now only exists in two files:

- `internal/logging/bridge.go`
- `internal/logging/level.go`

### Structured log calls

All ~527 log calls were converted from printf-style to structured key-value:

```go
// Before
loggers.Infof("Connected to %s on port %d", host, port)
loggers.Errorf("Failed to initialize: %s", err)

// After
logger.Info("connected", "host", host, "port", port)
logger.Error("failed to initialize", "error", err)
```

### OTel log export

When `USE_OTLP=true`, the main entry point creates an `OTelLogProvider` which
sets up an OTLP log exporter (gRPC or HTTP, matching `OTEL_EXPORTER_OTLP_PROTOCOL`).
The exporter's handler is composed with the console handler via `MultiHandler`,
so log records are sent to both the terminal and the OTel collector.

Standard OTel environment variables (`OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_SERVICE_NAME`, etc.) are honored by the
OTel SDK automatically.

### Configuration

- `OptLogLevel` in `config/config_field_types.go` now wraps `slog.Level`
  instead of `ldlog.LogLevel`
- `LOG_FORMAT` env var still controls text vs JSON output
- `LOG_LEVEL` env var still controls the global minimum level
- Per-environment `LD_LOG_LEVEL_*` vars still work

## New Dependencies

```
go.opentelemetry.io/contrib/bridges/otelslog
go.opentelemetry.io/otel/sdk/log
go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc
go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp
```

## Verification

- `go build ./...` -- clean
- `go vet ./...` -- only pre-existing warnings (unkeyed struct literals in benchmarks)
- `go test -count=1 ./...` -- all 21 packages pass, 0 failures
