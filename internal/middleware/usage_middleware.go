package middleware

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/launchdarkly/ld-relay/v9/internal/events"
)

// DynamicTrackUsageActivity is like TrackUsageActivity but determines the platform category
// at request time based on the credential type, for FDv2 unified client-side endpoints.
func DynamicTrackUsageActivity() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			instanceID := getInstanceID(req)
			tagsHeader := req.Header.Get(events.TagsHeader)
			platformCategory := clientPlatformCategory(ctx.Credential)
			mu := ctx.Env.GetMetricsManager()

			mu.UsageActivityCountMessage(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID, tagsHeader)

			next.ServeHTTP(w, req)
		})
	}
}

// DynamicUsageActivityStreamMonitoring is like UsageActivityStreamMonitoring but determines the
// platform category at request time based on the credential type, for FDv2 unified client-side endpoints.
func DynamicUsageActivityStreamMonitoring(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context())
		userAgent := getUserAgent(req)
		instanceID := getInstanceID(req)
		tagsHeader := req.Header.Get(events.TagsHeader)
		platformCategory := clientPlatformCategory(ctx.Credential)

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

func TrackUsageActivity(platformCategory string) mux.MiddlewareFunc {
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
