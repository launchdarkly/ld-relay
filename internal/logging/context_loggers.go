package logging

import (
	"context"
	"log/slog"
	"net/http"
)

type contextLoggerKey struct{}

// GetContextLogger returns the *slog.Logger associated with this HTTP request.
// If no logger was added to the request context, it returns slog.Default().
func GetContextLogger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextLoggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return slog.Default()
}

// ContextLoggerMiddleware attaches the given logger to each HTTP request's context.
func ContextLoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(context.WithValue(r.Context(), contextLoggerKey{}, logger))
			next.ServeHTTP(w, r)
		})
	}
}
