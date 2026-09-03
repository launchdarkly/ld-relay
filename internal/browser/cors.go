package browser

import (
	"context"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/internal/events"
)

const (
	// DefaultAllowedOrigin is the default origin string to use in CORS response headers.
	DefaultAllowedOrigin = "*"
)

type corsContextKeyType string

const (
	corsContextKey corsContextKeyType = "context"
	maxAge         string             = "300"
)

// DefaultAllowedHeaders is the default value of the CORS header Access-Control-Allow-Headers.
var DefaultAllowedHeaders = strings.Join([]string{ //nolint:gochecknoglobals
	"Cache-Control",
	"Content-Type",
	"Content-Length",
	"Accept-Encoding",
	"X-LaunchDarkly-User-Agent",
	"X-LaunchDarkly-Payload-ID",
	"X-LaunchDarkly-Wrapper",
	"X-LaunchDarkly-Instance-Id",
	events.EventSchemaHeader,
	events.TagsHeader,
}, ",")

// CORSContext represents a scope that has a specific set of allowed origins for CORS requests. This
// can be attached to a request context with WithCORSContext().
type CORSContext interface {
	AllowedOrigins() []string
	AllowedHeaders() []string
}

// GetCORSContext returns the CORSContext that has been attached to this Context with WithCORSContext(),
// or nil if none.
func GetCORSContext(ctx context.Context) CORSContext {
	if cc, ok := ctx.Value(corsContextKey).(CORSContext); ok {
		return cc
	}
	return nil
}

// WithCORSContext returns a copy of the parent context with the specified CORSContext attached.
func WithCORSContext(parent context.Context, cc CORSContext) context.Context {
	if cc == nil {
		return parent
	}
	return context.WithValue(parent, corsContextKey, cc)
}

// SetCORSHeaders sets a standard set of CORS headers on an HTTP response. This is meant to be the same
// behavior that the LaunchDarkly service endpoints uses for client-side JS requests.
//
// Because Access-Control-Allow-Origin is derived from the request's Origin header, "Origin" is added to
// the response's Vary header so that shared caches do not serve one origin's response to another.
func SetCORSHeaders(w http.ResponseWriter, origin string, extraAllowedHeaders []string) {
	AddVaryHeader(w, "Origin")
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "false")
	w.Header().Set("Access-Control-Max-Age", maxAge)
	allAllowedHeaders := DefaultAllowedHeaders
	if len(extraAllowedHeaders) > 0 {
		allAllowedHeaders = allAllowedHeaders + "," + strings.Join(extraAllowedHeaders, ",")
	}
	w.Header().Set("Access-Control-Allow-Headers", allAllowedHeaders)
	w.Header().Set("Access-Control-Expose-Headers", "Date")
}

// AddVaryHeader adds a field name to the response's Vary header, preserving any values already present
// and avoiding duplicates.
func AddVaryHeader(w http.ResponseWriter, fieldName string) {
	for _, existing := range w.Header().Values("Vary") {
		for _, value := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(value), fieldName) {
				return
			}
		}
	}
	w.Header().Add("Vary", fieldName)
}
