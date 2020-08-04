package relayenv

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/streams"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// EnvContext is the interface for all Relay operations that are specific to one configured LD environment.
//
// The EnvContext is normally associated with an LDClient instance from the Go SDK, and allows direct access
// to the DataStore that is used by the SDK client. However, these are created asynchronously since the client
// connection may take a while, so it is possible for the client and store references to be nil if initialization
// is not yet complete.
type EnvContext interface {
	io.Closer

	// GetName returns the configured name of the environment.
	GetName() string

	// GetCredentials returns the SDK key and other optional keys.
	GetCredentials() Credentials

	// GetClient returns the SDK client instance for this environment. This is nil if initialization is not yet
	// complete. Rather than providing the full client object, we use the simpler sdks.LDClientContext which
	// includes only the operations Relay needs to do.
	GetClient() sdks.LDClientContext

	// GetStore returns the SDK data store instance for this environment. This is nil if initialization is not
	// yet complete.
	GetStore() interfaces.DataStore

	// GetLoggers returns a Loggers instance that is specific to this environment. We configure each of these to
	// have its own prefix string and, optionally, its own log level.
	GetLoggers() ldlog.Loggers

	// GetHandler returns the HTTP handler for the specified kind of stream requests and credential for this
	// environment. If there is none, it returns a handler for a 404 status (not nil).
	GetStreamHandler(streams.StreamProvider, config.SDKCredential) http.Handler

	// GetEventDispatcher returns the object that proxies events for this environment.
	GetEventDispatcher() *events.EventDispatcher

	// GetJSClientContext returns the JSClientContext that is used for browser endpoints.
	GetJSClientContext() JSClientContext

	// GetMetricsContext returns the Context that should be used for OpenCensus operations related to this
	// environment.
	GetMetricsContext() context.Context

	// GetTTL returns the configured cache TTL for PHP SDK endpoints for this environment.
	GetTTL() time.Duration

	// GetInitError returns an error if initialization has failed, or nil otherwise.
	GetInitError() error

	// IsSecureMode returns true if client-side evaluation requests for this environment must have a valid
	// secure mode hash.
	IsSecureMode() bool
}

// Credentials encapsulates all the configured LD credentials for an environment. The SDK key is mandatory;
// the mobile key and environment ID may be omitted.
type Credentials struct {
	SDKKey        config.SDKKey
	MobileKey     config.MobileKey
	EnvironmentID config.EnvironmentID
}
