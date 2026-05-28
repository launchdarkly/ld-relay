package metrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/bridge/opencensus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// exporterType represents one of the kinds of OpenTelemetry exporter backends Relay supports.
type exporterType interface {
	// Returns the human-readable name, like "Datadog".
	getName() string

	// Checks the MetricsConfig and *if* this type of exporter is enabled in it, constructs an
	// implementation of the exporter interface containing the relevant configuration (but does not
	// register it yet). If this type of exporter is not enabled, returns (nil, nil).
	createExporterIfEnabled(config.MetricsConfig, ldlog.Loggers) (exporter, error)
}

// exporter is one configured destination for Relay's telemetry. Each exporter contributes zero or
// more OTel metric Readers and Span Processors to the shared providers built by the Manager.
type exporter interface {
	// Metric readers (e.g. an OTLP PeriodicReader, a Prometheus pull reader) to attach to the
	// shared MeterProvider. May be empty if the exporter only handles traces.
	metricReaders() []sdkmetric.Reader

	// Span processors to attach to the shared TracerProvider. May be empty if the exporter only
	// handles metrics.
	spanProcessors() []sdktrace.SpanProcessor

	// Additional resource attributes specific to this exporter (e.g. Datadog tags).
	resourceAttributes() *sdkresource.Resource

	// Called after the shared providers have been built and globals set. Use this for side
	// effects like binding an HTTP listener (Prometheus) — the providers themselves are already
	// active by the time this runs.
	register(loggers ldlog.Loggers) error

	// Release any resources held by this exporter. Provider shutdown is handled by the Manager.
	close() error
}

type exportersSet map[exporterType]exporter

func allExporterTypes() []exporterType {
	return []exporterType{datadogExporterType, prometheusExporterType, stackdriverExporterType, otelExporterType}
}

// telemetryProviders holds the OTel SDK providers built by registerExporters, along with the
// OpenCensus->OpenTelemetry bridge that forwards OC stats and spans into them.
type telemetryProviders struct {
	tracerProvider  *sdktrace.TracerProvider
	meterProvider   *sdkmetric.MeterProvider
	previousTextMap propagation.TextMapPropagator
}

// registerExporters constructs each enabled exporter, then builds a single shared TracerProvider
// and MeterProvider that fans telemetry out to all of them. The OpenCensus bridge is installed once
// so that Relay's existing OC-based instrumentation (measures, views, trace spans) is forwarded to
// every configured backend.
func registerExporters(
	exporterTypes []exporterType,
	c config.MetricsConfig,
	loggers ldlog.Loggers,
) (exportersSet, *telemetryProviders, error) {
	created := make(exportersSet)
	for _, t := range exporterTypes {
		e, err := t.createExporterIfEnabled(c, loggers)
		if err != nil {
			loggers.Errorf("Error creating %s metrics exporter: %s", t.getName(), err)
			closeExporters(created, loggers)
			return nil, nil, err
		}
		if e != nil {
			created[t] = e
		}
	}

	if len(created) == 0 {
		return created, nil, nil
	}

	var readers []sdkmetric.Reader
	var processors []sdktrace.SpanProcessor
	res := baseResource()
	for _, e := range created {
		readers = append(readers, e.metricReaders()...)
		processors = append(processors, e.spanProcessors()...)
		if extra := e.resourceAttributes(); extra != nil {
			merged, err := sdkresource.Merge(res, extra)
			if err == nil {
				res = merged
			}
		}
	}

	providers := &telemetryProviders{}
	if len(processors) > 0 {
		tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
		for _, p := range processors {
			tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(p))
		}
		providers.tracerProvider = sdktrace.NewTracerProvider(tpOpts...)
	}
	if len(readers) > 0 {
		mpOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
		for _, r := range readers {
			mpOpts = append(mpOpts, sdkmetric.WithReader(r))
		}
		providers.meterProvider = sdkmetric.NewMeterProvider(mpOpts...)
	}

	if providers.tracerProvider != nil {
		otel.SetTracerProvider(providers.tracerProvider)
		providers.previousTextMap = otel.GetTextMapPropagator()
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		opencensus.InstallTraceBridge(opencensus.WithTracerProvider(providers.tracerProvider))
	}
	if providers.meterProvider != nil {
		otel.SetMeterProvider(providers.meterProvider)
	}

	for _, t := range exporterTypes {
		e, ok := created[t]
		if !ok {
			continue
		}
		if err := e.register(loggers); err != nil {
			loggers.Errorf("Error registering %s metrics exporter: %s", t.getName(), err)
			closeExporters(created, loggers)
			shutdownProviders(providers, loggers)
			return nil, nil, err
		}
		loggers.Infof("Successfully registered %s metrics exporter", t.getName())
	}
	return created, providers, nil
}

// closeExporters attempts to close a set of exporters. Errors are logged but do not stop the
// operation.
func closeExporters(exporters exportersSet, loggers ldlog.Loggers) {
	for t, e := range exporters {
		if err := e.close(); err != nil {
			loggers.Errorf("Error closing %s metrics exporter: %s", t.getName(), err)
		}
	}
}

func shutdownProviders(p *telemetryProviders, loggers ldlog.Loggers) {
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errs []error
	if p.tracerProvider != nil {
		if p.previousTextMap != nil {
			otel.SetTextMapPropagator(p.previousTextMap)
		}
		if err := p.tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", err))
		}
	}
	if p.meterProvider != nil {
		if err := p.meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("meter provider shutdown: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		loggers.Errorf("Error shutting down telemetry providers: %s", err)
	}
}

func baseResource() *sdkresource.Resource {
	r, err := sdkresource.Merge(
		sdkresource.Default(),
		sdkresource.NewSchemaless(semconv.ServiceName(otelDefaultServiceName)),
	)
	if err != nil || r == nil {
		return sdkresource.Default()
	}
	return r
}

func getPrefix(configuredPrefix string) string {
	if configuredPrefix != "" {
		return configuredPrefix
	}
	return defaultMetricsPrefix
}
