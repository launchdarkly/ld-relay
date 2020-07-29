package cors

import (
	"context"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/internal/events"
)

const (
	// The default origin string to use in CORS response headers.
	DefaultAllowedOrigin = "*"
)

type contextKeyType string

const (
	contextKey contextKeyType = "context"
	maxAge     string         = "300"
)

var allowedHeadersList = []string{
	"Cache-Control",
	"Content-Type",
	"Content-Length",
	"Accept-Encoding",
	"X-LaunchDarkly-User-Agent",
	"X-LaunchDarkly-Payload-ID",
	"X-LaunchDarkly-Wrapper",
	events.EventSchemaHeader,
}

var allowedHeaders = strings.Join(allowedHeadersList, ",")

// RequestContext represents a scope that has a specific set of allowed origins for CORS requests. This
// can be attached to a request context with WithCORSContext().
type RequestContext interface {
	AllowedOrigins() []string
}

// GetCORSContext returns the CORSContext that has been attached to this Context with WithCORSContext(),
// or nil if none.
func GetCORSContext(ctx context.Context) RequestContext {
	if cc, ok := ctx.Value(contextKey).(RequestContext); ok {
		return cc
	}
	return nil
}

// WithCORSContext returns a copy of the parent context with the specified CORSContext attached.
func WithCORSContext(parent context.Context, cc RequestContext) context.Context {
	if cc == nil {
		return parent
	}
	return context.WithValue(parent, contextKey, cc)
}

// SetCORSHeaders sets a standard set of CORS headers on an HTTP response. This is meant to be the same
// behavior that the LaunchDarkly service endpoints uses for client-side JS requests.
func SetCORSHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "false")
	w.Header().Set("Access-Control-Max-Age", maxAge)
	w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
	w.Header().Set("Access-Control-Expose-Headers", "Date")
}
