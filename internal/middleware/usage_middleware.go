package middleware

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
)

func UsageActivityCount(platformCategory string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			instanceID := getInstanceID(req)
			tagsHeader := req.Header.Get(events.TagsHeader)
			mu := ctx.Env.GetMetricsManager()

			mu.UsageActivityCountMessage(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID, tagsHeader)

			next.ServeHTTP(w, req)
		})
	}
}

func UsageActivityStreamMonitoring(platformCategory string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context())
		userAgent := getUserAgent(req)
		instanceID := getInstanceID(req)
		tagsHeader := req.Header.Get(events.TagsHeader)

		if mu := ctx.Env.GetMetricsManager(); mu != nil {
			mu.UsageActivityStreamConnected(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID, tagsHeader)
		}

		defer func() {
			if mu := ctx.Env.GetMetricsManager(); mu != nil {
				mu.UsageActivityStreamDisconnected(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID, tagsHeader)
			}
		}()

		next.ServeHTTP(w, req)
	})
}
