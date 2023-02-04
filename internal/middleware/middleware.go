package middleware

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v7/internal/browser"
	"github.com/launchdarkly/ld-relay/v7/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v7/internal/sdks"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	ld "github.com/launchdarkly/go-server-sdk/v6"

	"github.com/gorilla/mux"
)

const (
	userAgentHeader   = "user-agent"
	ldUserAgentHeader = "X-LaunchDarkly-User-Agent"

	httpStatusMessageInvalidEnvCredential = "Relay Proxy does not recognize the client credential (missing or invalid Authorization header)"
	httpStatusMessageNotFullyConfigured   = "Relay Proxy is not yet fully initialized, does not have list of environments yet"
	httpStatusMessageMissingEnvURLParam   = "URL did not contain an environment ID"
	httpStatusMessageSDKClientNotInited   = "client was not initialized"
)

var (
	errInvalidBase64        = errors.New("string did not decode as valid base64")
	errInvalidContextBase64 = errors.New("context part of URL path did not decode as valid base64")
	errInvalidContextJSON   = errors.New("context part of URL path did not decode to valid evaluation context as JSON")
)

// RelayEnvironments defines the methods for looking up environments. This is represented as an interface
// so that test code can mock that capability.
type RelayEnvironments interface {
	GetEnvironment(config.SDKCredential) (env relayenv.EnvContext, fullyConfigured bool)
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
// using the appropriate kind of credential for the basictypes.SDKKind. If successful, it updates the request context
// so GetEnvContextInfo will return environment information. If not successful, it returns an error response.
func SelectEnvironmentByAuthorizationKey(sdkKind basictypes.SDKKind, envs RelayEnvironments) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			credential, err := sdks.GetCredential(sdkKind, req)
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			clientCtx, isConfigured := envs.GetEnvironment(credential)

			if !isConfigured {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(httpStatusMessageNotFullyConfigured))
				return
			}

			if clientCtx == nil || clientCtx.GetInitError() == ld.ErrInitializationFailed {
				// ErrInitializationFailed is what the SDK returns if it got a 401 error from LD.
				// Our error behavior here is slightly different for JS/browser clients
				if sdkKind == basictypes.JSClientSDK {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(httpStatusMessageMissingEnvURLParam))
				} else {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(httpStatusMessageInvalidEnvCredential))
				}
				return
			}

			if clientCtx.GetClient() == nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(httpStatusMessageSDKClientNotInited))
				return
			}

			contextInfo := EnvContextInfo{
				Env:        clientCtx,
				Credential: credential,
			}
			req = req.WithContext(WithEnvContextInfo(req.Context(), contextInfo))
			if sdkKind == basictypes.JSClientSDK {
				req = req.WithContext(browser.WithCORSContext(req.Context(), clientCtx.GetJSClientContext()))
			}
			next.ServeHTTP(w, req)
		})
	}
}

// CORS is a middleware function that sets the appropriate CORS headers on a browser response
// (not counting Access-Control-Allow-Methods, which is set by gorilla/mux's CORS middleware
// based on the route handlers we've defined).
//
// Also, if the HTTP method is OPTIONS, it will short-circuit the rest of the middleware chain
// so that the underlying handler is *not* called-- since an OPTIONS request should not do
// anything except set the CORS response headers. Therefore, we should always put the
// gorilla/mux CORS middleware before this one.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var domains []string
		var headers []string
		if corsContext := browser.GetCORSContext(r.Context()); corsContext != nil {
			domains = corsContext.AllowedOrigins()
			headers = corsContext.AllowedHeaders()
		}
		if len(domains) > 0 {
			domain := domains[0]
			for _, d := range domains {
				if r.Header.Get("Origin") == d {
					domain = d
					break
				}
			}
			browser.SetCORSHeaders(w, domain, headers)
		} else {
			origin := browser.DefaultAllowedOrigin
			if r.Header.Get("Origin") != "" {
				origin = r.Header.Get("Origin")
			}
			browser.SetCORSHeaders(w, origin, headers)
		}
		if r.Method != "OPTIONS" {
			next.ServeHTTP(w, r)
		}
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

// ContextFromBase64 decodes a base64-encoded go-server-sdk evaluation context.
// If any decoding/unmarshaling errors occur, or the decoded context is invalid by the rules of the Go SDK, an error is returned.
func ContextFromBase64(base64Context string) (ldcontext.Context, error) {
	var ldContext ldcontext.Context
	jsonStr, decodeErr := base64urlDecode(base64Context)
	if decodeErr != nil {
		return ldContext, errInvalidContextBase64
	}

	jsonErr := json.Unmarshal(jsonStr, &ldContext)

	if jsonErr != nil {
		return ldContext, errInvalidContextJSON
	}
	return ldContext, nil
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
