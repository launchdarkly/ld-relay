package middleware

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/mux"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core/internal/browser"
	"github.com/launchdarkly/ld-relay/v6/core/internal/metrics"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
)

const (
	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"
)

var (
	errInvalidBase64     = errors.New("string did not decode as valid base64")
	errInvalidUserBase64 = errors.New("user part of URL path did not decode as valid base64")
	errInvalidUserJSON   = errors.New("user part of URL path did not decode to valid user as JSON")
	errUserWithNoKey     = errors.New(`user must have a "key" attribute`)
)

// RelayEnvironments defines the methods for looking up environments. This is represented as an interface
// so that test code can mock that capability.
type RelayEnvironments interface {
	GetEnvironment(config.SDKCredential) relayenv.EnvContext
	GetAllEnvironments() []relayenv.EnvContext
}

// getUserAgent returns the X-LaunchDarkly-User-Agent if available, falling back to the normal "User-Agent" header
func getUserAgent(req *http.Request) string {
	if agent := req.Header.Get(ldUserAgentHeader); agent != "" {
		return agent
	}
	return req.Header.Get(userAgentHeader)
}

// Chain combines a series of middleware functions that will be applied in the same order.
func Chain(middlewares ...mux.MiddlewareFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		handler := next
		for i := len(middlewares) - 1; i >= 0; i-- {
			handler = middlewares[i](handler)
		}
		return handler
	}
}

// SelectEnvironmentByAuthorizationKey creates a middleware function that attempts to authenticate the request
// using the appropriate kind of credential for the sdks.Kind. If successful, it updates the request context
// so GetEnvContextInfo will return environment information. If not successful, it returns an error response.
func SelectEnvironmentByAuthorizationKey(sdkKind sdks.Kind, envs RelayEnvironments) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			credential, err := sdkKind.GetCredential(req)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			clientCtx := envs.GetEnvironment(credential)

			if clientCtx == nil {
				// Our error behavior here is slightly different for JS/browser clients
				if sdkKind == sdks.JSClient {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte("URL did not contain an environment ID"))
				} else {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte("ld-relay is not configured for the provided key"))
				}
				return
			}

			if clientCtx.GetClient() == nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("client was not initialized"))
				return
			}

			contextInfo := EnvContextInfo{
				Env:        clientCtx,
				Credential: credential,
			}
			req = req.WithContext(WithEnvContextInfo(req.Context(), contextInfo))
			next.ServeHTTP(w, req)
		})
	}
}

func withCount(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context()).Env
		userAgent := getUserAgent(req)
		metrics.WithCount(ctx.GetMetricsContext(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

// CountMobileConns is a middleware function that increments the total number of mobile connections,
// and also increments the number of active mobile connections until the handler ends.
func CountMobileConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.MobileConns), metrics.NewMobileConns)
}

// CountBrowserConns is a middleware function that increments the total number of browser connections,
// and also increments the number of active browser connections until the handler ends.
func CountBrowserConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.BrowserConns), metrics.NewBrowserConns)
}

// CountServerConns is a middleware function that increments the total number of server-side connections,
// and also increments the number of active server-side connections until the handler ends.
func CountServerConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.ServerConns), metrics.NewServerConns)
}

// RequestCount is a middleware function that increments the specified metric for each request.
func RequestCount(measure metrics.Measure) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := GetEnvContextInfo(req.Context())
			userAgent := getUserAgent(req)
			// Ignoring internal routing error that would have been ignored anyway
			route, _ := mux.CurrentRoute(req).GetPathTemplate()
			metrics.WithRouteCount(ctx.Env.GetMetricsContext(), userAgent, route, req.Method, func() {
				next.ServeHTTP(w, req)
			}, measure)
		})
	}
}

func withGauge(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := GetEnvContextInfo(req.Context())
		userAgent := getUserAgent(req)
		metrics.WithGauge(ctx.Env.GetMetricsContext(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

// CORS is a middleware function that sets the appropriate CORS headers on a browser response.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var domains []string
		if corsContext := browser.GetCORSContext(r.Context()); corsContext != nil {
			domains = corsContext.AllowedOrigins()
		}
		if len(domains) > 0 {
			for _, d := range domains {
				if r.Header.Get("Origin") == d {
					browser.SetCORSHeaders(w, d)
					return
				}
			}
			// Not a valid origin, set allowed origin to any allowed origin
			browser.SetCORSHeaders(w, domains[0])
		} else {
			origin := browser.DefaultAllowedOrigin
			if r.Header.Get("Origin") != "" {
				origin = r.Header.Get("Origin")
			}
			browser.SetCORSHeaders(w, origin)
		}
		next.ServeHTTP(w, r)
	})
}

// Streaming is a middleware function that sets the appropriate headers on a streaming response.
func Streaming(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// If Nginx is being used as a proxy/load balancer, adding this header tells it not to buffer this response because
		// it is a streaming response. If Nginx is not being used, this header has no effect.
		w.Header().Add("X-Accel-Buffering", "no")
		next.ServeHTTP(w, req)
	})
}

// UserV2FromBase64 decodes a base64-encoded go-server-sdk v2 user.
// If any decoding/unmarshaling errors occur or the user is missing the "key" attribute an error is returned.
func UserV2FromBase64(base64User string) (lduser.User, error) {
	var user lduser.User
	idStr, decodeErr := base64urlDecode(base64User)
	if decodeErr != nil {
		return user, errInvalidUserBase64
	}

	jsonErr := json.Unmarshal(idStr, &user)

	if jsonErr != nil {
		return user, errInvalidUserJSON
	}

	if user.GetKey() == "" {
		return user, errUserWithNoKey
	}
	return user, nil
}

func base64urlDecode(base64String string) ([]byte, error) {
	idStr, decodeErr := base64.URLEncoding.DecodeString(base64String)

	if decodeErr != nil {
		// base64String could be unpadded
		// see https://github.com/golang/go/issues/4237#issuecomment-267792481
		idStrRaw, decodeErrRaw := base64.RawURLEncoding.DecodeString(base64String)

		if decodeErrRaw != nil {
			return nil, errInvalidBase64
		}

		return idStrRaw, nil
	}

	return idStr, nil
}
