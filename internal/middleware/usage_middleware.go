package middleware

import (
	"net/http"

	"github.com/gorilla/mux"
)

func UsageActivityCount(platformCategory string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			instanceID := getInstanceID(req)
			mu := ctx.Env.GetMetricsManager()

			mu.UsageActivityCountMessage(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID)

			next.ServeHTTP(w, req)
		})
	}
}

func UsageActivityStreamMonitoring(platformCategory string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context())
		userAgent := getUserAgent(req)
		instanceID := getInstanceID(req)

		if mu := ctx.Env.GetMetricsManager(); mu != nil {
			mu.UsageActivityStreamConnected(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID)
		}

		defer func() {
			if mu := ctx.Env.GetMetricsManager(); mu != nil {
				mu.UsageActivityStreamDisconnected(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID)
			}
		}()

		next.ServeHTTP(w, req)
	})
}
