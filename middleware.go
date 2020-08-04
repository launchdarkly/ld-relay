package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/cors"
	"github.com/launchdarkly/ld-relay/v6/internal/metrics"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
)

func chainMiddleware(middlewares ...mux.MiddlewareFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		handler := next
		for i := len(middlewares) - 1; i >= 0; i-- {
			handler = middlewares[i](handler)
		}
		return handler
	}
}

func selectEnvironmentByAuthorizationKey(sdkKind sdks.Kind, envs RelayEnvironments) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			credential, err := sdkKind.GetCredential(req)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			clientCtx := envs.GetEnvironment(credential)

			if clientCtx == nil {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte("ld-relay is not configured for the provided key"))
				return
			}

			if clientCtx.GetClient() == nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("client was not initialized"))
				return
			}

			contextInfo := EnvContextInfo{
				Env:        clientCtx,
				Credential: credential,
			}
			req = req.WithContext(context.WithValue(req.Context(), contextKey, contextInfo))
			next.ServeHTTP(w, req)
		})
	}
}

func selectEnvironmentByEnvIDUrlParam(envs RelayEnvironments) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			envId, err := sdks.JSClient.GetCredential(req)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("URL did not contain an environment ID"))
				return
			}
			env := envs.GetEnvironment(envId)
			if env == nil {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(fmt.Sprintf("ld-relay is not configured for environment id %s", envId)))
				return
			}

			if env.GetClient() == nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("client was not initialized"))
				return
			}

			contextInfo := EnvContextInfo{
				Env:        env,
				Credential: envId,
			}
			req = req.WithContext(WithEnvContextInfo(req.Context(), contextInfo))
			req = req.WithContext(cors.WithCORSContext(req.Context(), env.GetJSClientContext()))
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

func countMobileConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.MobileConns), metrics.NewMobileConns)
}

func countBrowserConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.BrowserConns), metrics.NewBrowserConns)
}

func countServerConns(handler http.Handler) http.Handler {
	return withCount(withGauge(handler, metrics.ServerConns), metrics.NewServerConns)
}

func requestCountMiddleware(measure metrics.Measure) mux.MiddlewareFunc {
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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var domains []string
		if corsContext := cors.GetCORSContext(r.Context()); corsContext != nil {
			domains = corsContext.AllowedOrigins()
		}
		if len(domains) > 0 {
			for _, d := range domains {
				if r.Header.Get("Origin") == d {
					cors.SetCORSHeaders(w, d)
					return
				}
			}
			// Not a valid origin, set allowed origin to any allowed origin
			cors.SetCORSHeaders(w, domains[0])
		} else {
			origin := cors.DefaultAllowedOrigin
			if r.Header.Get("Origin") != "" {
				origin = r.Header.Get("Origin")
			}
			cors.SetCORSHeaders(w, origin)
		}
		next.ServeHTTP(w, r)
	})
}

func streamingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// If Nginx is being used as a proxy/load balancer, adding this header tells it not to buffer this response because
		// it is a streaming response. If Nginx is not being used, this header has no effect.
		w.Header().Add("X-Accel-Buffering", "no")
		next.ServeHTTP(w, req)
	})
}

// UserV2FromBase64 decodes a base64-encoded go-server-sdk v2 user.
// If any decoding/unmarshaling errors occur or
// the user is missing the 'key' attribute an error is returned.
func UserV2FromBase64(base64User string) (lduser.User, error) {
	var user lduser.User
	idStr, decodeErr := base64urlDecode(base64User)
	if decodeErr != nil {
		return user, errors.New("User part of url path did not decode as valid base64")
	}

	jsonErr := json.Unmarshal(idStr, &user)

	if jsonErr != nil {
		return user, errors.New("User part of url path did not decode to valid user as json")
	}

	if user.GetKey() == "" {
		return user, errors.New("User must have a 'key' attribute")
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
			return nil, errors.New("String did not decode as valid base64")
		}

		return idStrRaw, nil
	}

	return idStr, nil
}
