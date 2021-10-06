package browser

import (
	"context"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/events"
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

var allowedHeaders = strings.Join([]string{ //nolint:gochecknoglobals
	"Cache-Control",
	"Content-Type",
	"Content-Length",
	"Accept-Encoding",
	"X-LaunchDarkly-User-Agent",
	"X-LaunchDarkly-Payload-ID",
	"X-LaunchDarkly-Wrapper",
	events.EventSchemaHeader,
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
func SetCORSHeaders(w http.ResponseWriter, origin string, extraAllowedHeaders []string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "false")
	w.Header().Set("Access-Control-Max-Age", maxAge)
	allAllowedHeaders := allowedHeaders
	if len(extraAllowedHeaders) > 0 {
		allAllowedHeaders = allAllowedHeaders + "," + strings.Join(extraAllowedHeaders, ",")
	}
	w.Header().Set("Access-Control-Allow-Headers", allAllowedHeaders)
	w.Header().Set("Access-Control-Expose-Headers", "Date")
}
