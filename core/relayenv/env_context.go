package relayenv

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/streams"
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

	// GetName returns the name of the environment. This is the display name that was specified in the
	// configuration, or, for an auto-configured environment, the concenated project + environment names.
	GetName() string

	// SetName updates the name of the environment.
	SetName(string)

	// GetCredentials returns all currently enabled and non-deprecated credentials for the environment.
	GetCredentials() []config.SDKCredential

	// AddCredential adds a new credential for the environment.
	//
	// If the credential is an SDK key, then a new SDK client is started with that SDK key, and event forwarding
	// to server-side endpoints is switched to use the new key.
	AddCredential(config.SDKCredential)

	// RemoveCredential removes a credential from the environment. Any active stream connections using that
	// credential are immediately dropped.
	//
	// If the credential is an SDK key, then the SDK client that we started with that SDK key is disposed of.
	RemoveCredential(config.SDKCredential)

	// DeprecateCredential marks an existing credential as not being a preferred one, without removing it or
	// dropping any connections. It will no longer be included in the return value of GetCredentials(). This is
	// used in Relay Proxy Enterprise when an SDK key is being changed but the old key has not expired yet.
	DeprecateCredential(config.SDKCredential)

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

	// SetTTL changes the configured cache TTL for PHP SDK endpoints for this environment.
	SetTTL(time.Duration)

	// GetInitError returns an error if initialization has failed, or nil otherwise.
	GetInitError() error

	// IsSecureMode returns true if client-side evaluation requests for this environment must have a valid
	// secure mode hash.
	IsSecureMode() bool

	// SetSecureMode changes the secure mode setting.
	SetSecureMode(bool)

	// GetCreationTime returns the time that this EnvContext was created.
	GetCreationTime() time.Time
}

// GetEnvironmentID is a helper for extracting the EnvironmentID, if any, from the set of credentials.
func GetEnvironmentID(env EnvContext) config.EnvironmentID {
	for _, c := range env.GetCredentials() {
		if e, ok := c.(config.EnvironmentID); ok {
			return e
		}
	}
	return ""
}
