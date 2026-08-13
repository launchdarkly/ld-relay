package tracing

import (
	"context"
	"unicode/utf8"

	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// NewUTF8SanitizingExporter wraps a span exporter and replaces invalid UTF-8 before the spans reach it.
//
// OTLP cannot serialize a string field that holds a byte outside UTF-8. The marshal fails for the whole
// export batch, so one bad value costs every span batched alongside it. Several attributes the HTTP
// instrumentation records come from request data that carries no such restriction: the Host, the
// User-Agent, X-Forwarded-For, the peer address, the protocol version, and the percent-decoded path.
//
// This sanitizes whatever the instrumentation recorded rather than a list of attributes named in
// advance. Relay maintained such a list twice and both times it was missing an attribute. A list also
// has to be revisited on every dependency upgrade, and nothing fails when it is not.
//
// It runs at export rather than at span start for two reasons. The batch processor exports on its own
// goroutine, so no request pays for it. And a span reaching an exporter is an immutable snapshot whose
// attributes are free to read, whereas reading them from a live span allocates a map each time to
// deduplicate them.
//
// Attribute keys are not checked. Every key comes from an instrumentation constant, so a key cannot
// carry request data.
func NewUTF8SanitizingExporter(wrapped sdktrace.SpanExporter) sdktrace.SpanExporter {
	return sanitizingExporter{wrapped: wrapped}
}

type sanitizingExporter struct {
	wrapped sdktrace.SpanExporter
}

func (e sanitizingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	// The batch belongs to the caller, which reuses it, so replace a span only in a copy.
	var sanitized []sdktrace.ReadOnlySpan
	for i, span := range spans {
		repaired, changed := sanitizeSpan(span)
		if !changed {
			continue
		}
		if sanitized == nil {
			sanitized = append(make([]sdktrace.ReadOnlySpan, 0, len(spans)), spans...)
		}
		sanitized[i] = repaired
	}
	if sanitized != nil {
		spans = sanitized
	}
	return e.wrapped.ExportSpans(ctx, spans)
}

func (e sanitizingExporter) Shutdown(ctx context.Context) error {
	return e.wrapped.Shutdown(ctx)
}

// sanitizedSpan reports repaired values in place of the ones the span recorded.
//
// It embeds the interface rather than listing its methods, so a method added to ReadOnlySpan in a later
// SDK release is forwarded to the original span instead of breaking the build.
type sanitizedSpan struct {
	sdktrace.ReadOnlySpan
	name       string
	attributes []attribute.KeyValue
	status     sdktrace.Status
	events     []sdktrace.Event
	links      []sdktrace.Link
}

func (s sanitizedSpan) Name() string                     { return s.name }
func (s sanitizedSpan) Attributes() []attribute.KeyValue { return s.attributes }
func (s sanitizedSpan) Status() sdktrace.Status          { return s.status }
func (s sanitizedSpan) Events() []sdktrace.Event         { return s.events }
func (s sanitizedSpan) Links() []sdktrace.Link           { return s.links }

// sanitizeSpan returns a span whose every string field is valid, and reports whether anything changed.
// It returns the original span when there is nothing to repair, which is the usual case.
func sanitizeSpan(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, bool) {
	name, nameChanged := sanitizeString(span.Name())
	attributes, attributesChanged := sanitizeAttrs(span.Attributes())

	status := span.Status()
	description, statusChanged := sanitizeString(status.Description)
	if statusChanged {
		status.Description = description
	}

	events, eventsChanged := sanitizeEvents(span.Events())
	links, linksChanged := sanitizeLinks(span.Links())

	if !nameChanged && !attributesChanged && !statusChanged && !eventsChanged && !linksChanged {
		return span, false
	}
	return sanitizedSpan{
		ReadOnlySpan: span,
		name:         name,
		attributes:   attributes,
		status:       status,
		events:       events,
		links:        links,
	}, true
}

func sanitizeString(v string) (string, bool) {
	if utf8.ValidString(v) {
		return v, false
	}
	return util.SanitizeUTF8(v), true
}

// sanitizeAttrs repairs string and string-slice values, and copies only when there is something to
// repair.
func sanitizeAttrs(attrs []attribute.KeyValue) ([]attribute.KeyValue, bool) {
	changed := false
	for i, kv := range attrs {
		var repaired attribute.KeyValue
		switch kv.Value.Type() {
		case attribute.STRING:
			value, dirty := sanitizeString(kv.Value.AsString())
			if !dirty {
				continue
			}
			repaired = kv.Key.String(value)
		case attribute.STRINGSLICE:
			values, dirty := sanitizeStrings(kv.Value.AsStringSlice())
			if !dirty {
				continue
			}
			repaired = kv.Key.StringSlice(values)
		default:
			// Every other type is numeric or boolean, so it cannot hold invalid UTF-8.
			continue
		}
		if !changed {
			attrs = append([]attribute.KeyValue(nil), attrs...)
			changed = true
		}
		attrs[i] = repaired
	}
	return attrs, changed
}

func sanitizeStrings(values []string) ([]string, bool) {
	changed := false
	for i, value := range values {
		repaired, dirty := sanitizeString(value)
		if !dirty {
			continue
		}
		if !changed {
			values = append([]string(nil), values...)
			changed = true
		}
		values[i] = repaired
	}
	return values, changed
}

func sanitizeEvents(events []sdktrace.Event) ([]sdktrace.Event, bool) {
	changed := false
	for i, event := range events {
		name, nameDirty := sanitizeString(event.Name)
		attrs, attrsDirty := sanitizeAttrs(event.Attributes)
		if !nameDirty && !attrsDirty {
			continue
		}
		if !changed {
			events = append([]sdktrace.Event(nil), events...)
			changed = true
		}
		events[i].Name = name
		events[i].Attributes = attrs
	}
	return events, changed
}

func sanitizeLinks(links []sdktrace.Link) ([]sdktrace.Link, bool) {
	changed := false
	for i, link := range links {
		attrs, dirty := sanitizeAttrs(link.Attributes)
		if !dirty {
			continue
		}
		if !changed {
			links = append([]sdktrace.Link(nil), links...)
			changed = true
		}
		links[i].Attributes = attrs
	}
	return links, changed
}
