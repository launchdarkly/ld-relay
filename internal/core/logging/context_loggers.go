package logging

import (
	"context"
	"net/http"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type contextLoggersName string

const globalContextLoggersName contextLoggersName = "GlobalContextLoggers"

// GetGlobalContextLoggers returns the Loggers associated with this HTTP request for Relay's global logging.
// If no such context information was added to the request, it returns disabled loggers.
func GetGlobalContextLoggers(ctx context.Context) ldlog.Loggers {
	if value := ctx.Value(globalContextLoggersName); value != nil {
		if l, ok := value.(ldlog.Loggers); ok {
			return l
		}
	}
	return ldlog.NewDisabledLoggers()
}

// GlobalContextLoggersMiddleware attaches global logging context to each HTTP request.
func GlobalContextLoggersMiddleware(loggers ldlog.Loggers) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r1 := r.WithContext(context.WithValue(r.Context(), globalContextLoggersName, loggers))
			next.ServeHTTP(w, r1)
		})
	}
}
