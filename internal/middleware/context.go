package middleware

import (
	"context"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
)

type contextKeyType string

const contextKey contextKeyType = "context"

// EnvContextInfo is data that we attach to the current HTTP request to indicate which environment it
// is related to.
type EnvContextInfo struct {
	// Env is an existing EnvContext object for the environment.
	Env relayenv.EnvContext

	// Credential is the SDK key, mobile key, or environment ID that was used in the request.
	Credential credential.SDKCredential
}

// GetEnvContextInfo returns the EnvContextInfo that is attached to the specified Context (normally
// obtained from a request as request.Context()). It panics if there is none, since this context data
// is supposed to be injected by our middleware and our handlers cannot work without it.
func GetEnvContextInfo(ctx context.Context) EnvContextInfo {
	return ctx.Value(contextKey).(EnvContextInfo)
}

// WithEnvContextInfo returns a new Context with the EnvContextInfo added.
func WithEnvContextInfo(ctx context.Context, info EnvContextInfo) context.Context {
	return context.WithValue(ctx, contextKey, info)
}
