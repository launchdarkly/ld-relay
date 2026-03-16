package middleware

import (
	"net/http"

	"github.com/gorilla/mux"
)

// DynamicTrackUsageActivity is like TrackUsageActivity but determines the platform category
// at request time based on the credential type, for FDv2 unified client-side endpoints.
func DynamicTrackUsageActivity() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			instanceID := getInstanceID(req)
			platformCategory := clientPlatformCategory(ctx.Credential)
			mu := ctx.Env.GetMetricsManager()

			mu.TrackUsageActivityMessage(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID)

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
		platformCategory := clientPlatformCategory(ctx.Credential)

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

func TrackUsageActivity(platformCategory string) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			instanceID := getInstanceID(req)
			mu := ctx.Env.GetMetricsManager()

			mu.TrackUsageActivityMessage(ctx.Env.GetIdentifiers().GetDisplayName(), userAgent, platformCategory, instanceID)

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
