package relayenv

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/streams"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ldeval "github.com/launchdarkly/go-server-sdk-evaluation/v2"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces"
)

// EnvContext is the interface for all Relay operations that are specific to one configured LD environment.
//
// The EnvContext is normally associated with an LDClient instance from the Go SDK, and allows direct access
// to the DataStore that is used by the SDK client. However, these are created asynchronously since the client
// connection may take a while, so it is possible for the client and store references to be nil if initialization
// is not yet complete.
type EnvContext interface {
	io.Closer

	// GetIdentifiers returns information about the environment and project names and keys.
	GetIdentifiers() EnvIdentifiers

	// SetIdentifiers updates the environment and project names and keys.
	SetIdentifiers(EnvIdentifiers)

	// GetCredentials returns all currently enabled and non-deprecated credentials for the environment.
	GetCredentials() []config.SDKCredential

	// GetDeprecatedCredentials returns all deprecated and not-yet-removed credentials for the environment.
	GetDeprecatedCredentials() []config.SDKCredential

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

	// GetEvaluator returns an instance of the evaluation engine for evaluating feature flags in this environment.
	// This is nil if initialization is not yet complete.
	GetEvaluator() ldeval.Evaluator

	// GetBigSegmentStore returns the big segment data store instance for this environment. If a big
	// segment store is not configured this returns nil.
	GetBigSegmentStore() bigsegments.BigSegmentStore

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

	// GetDataStoreInfo returns information about the environment's data store.
	GetDataStoreInfo() sdks.DataStoreEnvironmentInfo

	// FlushMetricsEvents is used in testing to ensure that metrics events are delivered promptly.
	FlushMetricsEvents()
}

// EnvIdentifiers contains environment and project name and key properties.
//
// When running in Relay Proxy Enterprise's auto-configuration mode, EnvKey, EnvName, ProjKey, and ProjName are
// copied from the LaunchDarkly dashboard settings. Otherwise, those are all blank and ConfiguredName is set in
// the local configuration.
type EnvIdentifiers struct {
	// EnvKey is the environment key (normally a lowercase string like "production").
	EnvKey string

	// EnvName is the environment name (normally a title-cased string like "Production").
	EnvName string

	// ProjKey is the project key (normally a lowercase string like "my-application").
	ProjKey string

	// ProjName is the project name (normally a title-cased string like "My Application").
	ProjName string

	// ConfiguredName is a human-readable unique name for this environment, if the user specified one. When
	// using a local configuration, this is always set; in auto-configuration mode, it is always empty (but
	// EnvIdentifiers.GetDisplayName() will compute one).
	ConfiguredName string
}

// GetDisplayName returns a human-readable unique name for this environment. If none was set in the
// configuration, it computes one in the format "ProjName EnvName".
func (ei EnvIdentifiers) GetDisplayName() string {
	if ei.ConfiguredName == "" {
		return fmt.Sprintf("%s %s", ei.ProjName, ei.EnvName)
	}
	return ei.ConfiguredName
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
