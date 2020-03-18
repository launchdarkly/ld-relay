package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/mux"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/metrics"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/version"
)

type corsContext interface {
	AllowedOrigins() []string
}

func chainMiddleware(middlewares ...mux.MiddlewareFunc) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		handler := next
		for i := len(middlewares) - 1; i >= 0; i-- {
			handler = middlewares[i](handler)
		}
		return handler
	}
}

type clientMux struct {
	clientContextByKey map[string]*clientContextImpl
}

func (m clientMux) getStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	envs := make(map[string]environmentStatus)

	healthy := true
	for _, clientCtx := range m.clientContextByKey {
		var status environmentStatus
		if clientCtx.envId != nil {
			status.EnvId = *clientCtx.envId
		}
		if clientCtx.mobileKey != nil {
			status.MobileKey = obscureKey(*clientCtx.mobileKey)
		}
		status.SdkKey = obscureKey(clientCtx.sdkKey)
		client := clientCtx.getClient()
		if client == nil || !client.Initialized() {
			status.Status = "disconnected"
			healthy = false
		} else {
			status.Status = "connected"
		}
		envs[clientCtx.name] = status
	}

	resp := struct {
		Environments  map[string]environmentStatus `json:"environments"`
		Status        string                       `json:"status"`
		Version       string                       `json:"version"`
		ClientVersion string                       `json:"clientVersion"`
	}{
		Environments:  envs,
		Version:       version.Version,
		ClientVersion: ld.Version,
	}

	if healthy {
		resp.Status = "healthy"
	} else {
		resp.Status = "degraded"
	}

	data, _ := json.Marshal(resp)

	w.Write(data)
}

func (m clientMux) selectClientByAuthorizationKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		authKey, err := fetchAuthToken(req)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		clientCtx := m.clientContextByKey[authKey]

		if clientCtx == nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("ld-relay is not configured for the provided key"))
			return
		}

		if clientCtx.getClient() == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("client was not initialized"))
			return
		}

		req = req.WithContext(context.WithValue(req.Context(), contextKey, clientCtx))
		next.ServeHTTP(w, req)
	})
}

func getClientContext(req *http.Request) clientContext {
	return req.Context().Value(contextKey).(clientContext)
}

func withCount(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := getClientContext(req)
		userAgent := getUserAgent(req)
		metrics.WithCount(ctx.getMetricsCtx(), userAgent, func() {
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
			ctx := getClientContext(req)
			userAgent := getUserAgent(req)
			// Ignoring internal routing error that would have been ignored anyway
			route, _ := mux.CurrentRoute(req).GetPathTemplate()
			metrics.WithRouteCount(ctx.getMetricsCtx(), userAgent, route, req.Method, func() {
				next.ServeHTTP(w, req)
			}, measure)
		})
	}
}

func withGauge(handler http.Handler, measure metrics.Measure) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := getClientContext(req)
		userAgent := getUserAgent(req)
		metrics.WithGauge(ctx.getMetricsCtx(), userAgent, func() {
			handler.ServeHTTP(w, req)
		}, measure)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var domains []string
		if context, ok := r.Context().Value(contextKey).(corsContext); ok {
			domains = context.AllowedOrigins()
		}
		if len(domains) > 0 {
			for _, d := range domains {
				if r.Header.Get("Origin") == d {
					setCorsHeaders(w, d)
					return
				}
			}
			// Not a valid origin, set allowed origin to any allowed origin
			setCorsHeaders(w, domains[0])
		} else {
			origin := defaultAllowedOrigin
			if r.Header.Get("Origin") != "" {
				origin = r.Header.Get("Origin")
			}
			setCorsHeaders(w, origin)
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
func UserV2FromBase64(base64User string) (*ld.User, error) {
	var user ld.User
	idStr, decodeErr := base64urlDecode(base64User)
	if decodeErr != nil {
		return nil, errors.New("User part of url path did not decode as valid base64")
	}

	jsonErr := json.Unmarshal(idStr, &user)

	if jsonErr != nil {
		return nil, errors.New("User part of url path did not decode to valid user as json")
	}

	if user.Key == nil { //nolint:staticcheck // direct access to user.Key is deprecated
		return nil, errors.New("User must have a 'key' attribute")
	}
	return &user, nil
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

func fetchAuthToken(req *http.Request) (string, error) {
	authHdr := req.Header.Get("Authorization")
	match := uuidHeaderPattern.FindStringSubmatch(authHdr)

	// successfully matched UUID from header
	if len(match) == 2 {
		return match[1], nil
	}

	return "", errors.New("No valid token found")
}
