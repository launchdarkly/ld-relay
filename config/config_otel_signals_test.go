package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// USE_OTLP stays the master switch: with it off, no signal is exported no matter what the per-signal
// settings say. With it on, an unset per-signal setting means the signal is exported, which is the
// OpenTelemetry specification's "otlp" default and is what USE_OTLP alone used to mean.
func TestSignalExportDecisions(t *testing.T) {
	tests := []struct {
		name    string
		config  OpenTelemetryConfig
		logs    bool
		traces  bool
		metrics bool
	}{
		{
			name:   "OpenTelemetry disabled exports nothing",
			config: OpenTelemetryConfig{},
		},
		{
			name: "OpenTelemetry disabled ignores per-signal settings",
			config: OpenTelemetryConfig{
				LogsExporter:    "otlp",
				TracesExporter:  "otlp",
				MetricsExporter: "otlp",
			},
		},
		{
			name:    "unset per-signal settings export every signal",
			config:  OpenTelemetryConfig{Enabled: true},
			logs:    true,
			traces:  true,
			metrics: true,
		},
		{
			name: "explicit otlp exports every signal",
			config: OpenTelemetryConfig{
				Enabled:         true,
				LogsExporter:    "otlp",
				TracesExporter:  "otlp",
				MetricsExporter: "otlp",
			},
			logs:    true,
			traces:  true,
			metrics: true,
		},
		{
			name: "none disables every signal",
			config: OpenTelemetryConfig{
				Enabled:         true,
				LogsExporter:    "none",
				TracesExporter:  "none",
				MetricsExporter: "none",
			},
		},
		{
			name: "signals are independent",
			config: OpenTelemetryConfig{
				Enabled:      true,
				LogsExporter: "none",
			},
			traces:  true,
			metrics: true,
		},
		{
			name: "values are case-insensitive and tolerate surrounding space",
			config: OpenTelemetryConfig{
				Enabled:         true,
				LogsExporter:    "NONE",
				TracesExporter:  "  none  ",
				MetricsExporter: "OTLP",
			},
			metrics: true,
		},
		{
			// These variables are routinely set host- or pod-wide for other workloads. An exporter
			// Relay does not implement has to leave the signal exporting over OTLP, which is both
			// what the specification requires of an unrecognized enum value and what Relay did
			// before it read these variables at all.
			name: "exporters Relay does not implement are ignored, not treated as off",
			config: OpenTelemetryConfig{
				Enabled:         true,
				LogsExporter:    "console",
				TracesExporter:  "zipkin",
				MetricsExporter: "prometheus",
			},
			logs:    true,
			traces:  true,
			metrics: true,
		},
		{
			// The comma-separated list form is a MAY in the specification and Relay does not parse
			// it, so a list falls into the unrecognized case above rather than turning a signal off.
			name: "a list is unrecognized rather than partially honored",
			config: OpenTelemetryConfig{
				Enabled:      true,
				LogsExporter: "otlp,console",
			},
			logs:    true,
			traces:  true,
			metrics: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.logs, test.config.ExportLogs(), "ExportLogs")
			assert.Equal(t, test.traces, test.config.ExportTraces(), "ExportTraces")
			assert.Equal(t, test.metrics, test.config.ExportMetrics(), "ExportMetrics")
		})
	}
}
