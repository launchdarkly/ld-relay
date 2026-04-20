package middleware

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	ct "github.com/launchdarkly/go-configtypes"

	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/browser"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	ld "github.com/launchdarkly/go-server-sdk/v7"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/otel/codes"
)

const (
	userAgentHeader    = "user-agent"
	ldUserAgentHeader  = "X-LaunchDarkly-User-Agent"
	ldInstanceIDHeader = "X-LaunchDarkly-Instance-Id"
	ldWrapperHeader    = "X-LaunchDarkly-Wrapper"
	ldTagsHeader       = "X-LaunchDarkly-Tags"
	ldEnvIDHeader      = "X-LD-EnvId"

	httpStatusMessageInvalidEnvCredential  = "Relay Proxy does not recognize the client credential (missing or invalid Authorization header)"
	httpStatusMessageNotFullyConfigured    = "Relay Proxy is not yet fully initialized, does not have list of environments yet"
	httpStatusMessagePayloadFilterNotFound = "Relay Proxy recognizes the provided credential, but the payload filter was not found"
	httpStatusMessageMissingEnvURLParam    = "URL did not contain an environment ID"
	httpStatusMessageSDKClientNotInited    = "client was not initialized"
)

var (
	errInvalidBase64        = errors.New("string did not decode as valid base64")
	errInvalidContextBase64 = errors.New("context part of URL path did not decode as valid base64")
	errInvalidContextJSON   = errors.New("context part of URL path did not decode to valid evaluation context as JSON")
)

// RelayEnvironments defines the methods for looking up environments. This is represented as an interface
// so that test code can mock that capability.
type RelayEnvironments interface {
	// GetEnvironment returns the potentially filtered environment corresponding to scopedCred, or an error if no matching
	// environment could be found.
	GetEnvironment(scopedCred sdkauth.ScopedCredential) (env relayenv.EnvContext, err error)
	// IsNotReady should return true if the error returned by GetEnvironment represents the fact that Relay is not yet
	// fully configured.
	IsNotReady(error) bool
	// IsPayloadFilterNotFound should return true if the error returned by GetEnvironment represents the fact that
	// the credential was correct, but the payload filter was not found.
	IsPayloadFilterNotFound(error) bool
}

// getUserAgent returns the X-LaunchDarkly-User-Agent if available, falling back to the normal "User-Agent" header
func getUserAgent(req *http.Request) string {
	if agent := req.Header.Get(ldUserAgentHeader); agent != "" {
		return agent
	}
	return req.Header.Get(userAgentHeader)
}

// getInstanceID returns the X-LaunchDarkly-Instance-Id if available
func getInstanceID(req *http.Request) string {
	return req.Header.Get(ldInstanceIDHeader)
}

// getSDKWrapper returns the X-LaunchDarkly-Wrapper if available
func getSDKWrapper(req *http.Request) string {
	return req.Header.Get(ldWrapperHeader)
}

// parseApplicationTags extracts the application ID and version from the
// X-LaunchDarkly-Tags header. The header format is space-separated key/value
// pairs like "application-id/my-app application-version/1.0.0".
func parseApplicationTags(req *http.Request) (applicationID, applicationVersion string) {
	tags := req.Header.Get(ldTagsHeader)
	for _, part := range strings.Split(tags, " ") {
		if k, v, ok := strings.Cut(part, "/"); ok {
			switch k {
			case "application-id":
				applicationID = v
			case "application-version":
				applicationVersion = v
			}
		}
	}
	return
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
			ctx, span := tracing.Tracer().Start(req.Context(), tracing.SpanAuth)
			defer span.End()
			span.SetAttributes(tracing.SDKKindKey.String(string(sdkKind)))
			req = req.WithContext(ctx)

			credential, err := sdks.GetCredential(sdkKind, req)
			if err != nil {
				span.SetAttributes(tracing.AuthResultKey.String("invalid_credential"))
				span.SetStatus(codes.Error, "invalid credential")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			queryValues := req.URL.Query()
			filterKey := config.FilterKey(queryValues.Get("filter"))

			clientCtx, err := envs.GetEnvironment(sdkauth.NewScoped(filterKey, credential))

			if envs.IsNotReady(err) {
				span.SetAttributes(tracing.AuthResultKey.String("not_ready"))
				span.SetStatus(codes.Error, "not ready")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(httpStatusMessageNotFullyConfigured))
				return
			}

			if envs.IsPayloadFilterNotFound(err) {
				span.SetAttributes(tracing.AuthResultKey.String("filter_not_found"))
				span.SetStatus(codes.Error, "filter not found")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(httpStatusMessagePayloadFilterNotFound))
				return
			}

			if err != nil || clientCtx.GetInitError() == ld.ErrInitializationFailed {
				span.SetAttributes(tracing.AuthResultKey.String("not_found"))
				span.SetStatus(codes.Error, "environment not found")
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
				span.SetAttributes(tracing.AuthResultKey.String("client_not_initialized"))
				span.SetStatus(codes.Error, "client not initialized")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(httpStatusMessageSDKClientNotInited))
				return
			}

			if envID := relayenv.GetEnvironmentID(clientCtx); envID != "" {
				w.Header().Set(ldEnvIDHeader, string(envID))
			}

			span.SetAttributes(tracing.AuthResultKey.String("success"))

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

// SelectEnvironmentByClientSideAuth creates a middleware function that authenticates the request
// using either a mobile key or environment ID. The credential type is determined by inspecting the
// token value: tokens starting with "mob-" are treated as mobile keys, otherwise as environment IDs.
// The token is extracted from the Authorization header or the "auth" query parameter.
func SelectEnvironmentByClientSideAuth(envs RelayEnvironments) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// OPTIONS preflight requests don't carry Authorization headers, so skip auth.
			// The CORS middleware later in the chain will handle the response.
			if req.Method == "OPTIONS" {
				next.ServeHTTP(w, req)
				return
			}

			ctx, span := tracing.Tracer().Start(req.Context(), tracing.SpanAuth)
			defer span.End()
			req = req.WithContext(ctx)

			token, err := sdks.FetchClientSideAuthToken(req)
			if err != nil {
				span.SetAttributes(tracing.AuthResultKey.String("invalid_credential"))
				span.SetStatus(codes.Error, "invalid credential")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(httpStatusMessageInvalidEnvCredential))
				return
			}

			var cred credential.SDKCredential
			if strings.HasPrefix(token, "mob-") {
				cred = config.MobileKey(token)
				span.SetAttributes(tracing.SDKKindKey.String(string(basictypes.MobileSDK)))
			} else {
				cred = config.EnvironmentID(token)
				span.SetAttributes(tracing.SDKKindKey.String(string(basictypes.JSClientSDK)))
			}

			queryValues := req.URL.Query()
			filterKey := config.FilterKey(queryValues.Get("filter"))

			clientCtx, err := envs.GetEnvironment(sdkauth.NewScoped(filterKey, cred))

			if envs.IsNotReady(err) {
				span.SetAttributes(tracing.AuthResultKey.String("not_ready"))
				span.SetStatus(codes.Error, "not ready")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(httpStatusMessageNotFullyConfigured))
				return
			}

			if envs.IsPayloadFilterNotFound(err) {
				span.SetAttributes(tracing.AuthResultKey.String("filter_not_found"))
				span.SetStatus(codes.Error, "filter not found")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(httpStatusMessagePayloadFilterNotFound))
				return
			}

			if err != nil || clientCtx.GetInitError() == ld.ErrInitializationFailed {
				span.SetAttributes(tracing.AuthResultKey.String("not_found"))
				span.SetStatus(codes.Error, "environment not found")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(httpStatusMessageInvalidEnvCredential))
				return
			}

			if clientCtx.GetClient() == nil {
				span.SetAttributes(tracing.AuthResultKey.String("client_not_initialized"))
				span.SetStatus(codes.Error, "client not initialized")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(httpStatusMessageSDKClientNotInited))
				return
			}

			if envID := relayenv.GetEnvironmentID(clientCtx); envID != "" {
				w.Header().Set(ldEnvIDHeader, string(envID))
			}

			span.SetAttributes(tracing.AuthResultKey.String("success"))

			contextInfo := EnvContextInfo{
				Env:        clientCtx,
				Credential: cred,
			}
			req = req.WithContext(WithEnvContextInfo(req.Context(), contextInfo))
			req = req.WithContext(browser.WithCORSContext(req.Context(), clientCtx.GetJSClientContext()))
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

func GzipMiddleware(maxInboundPayloadSize ct.OptBase2Bytes) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isGzipped := strings.Contains(r.Header.Get("Content-Encoding"), "gzip")
			reader, err := util.NewReader(r.Body, isGzipped, maxInboundPayloadSize)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = reader
			next.ServeHTTP(w, r)
		})
	}
}
