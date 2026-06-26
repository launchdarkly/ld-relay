package relayenv

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	ldeval "github.com/launchdarkly/go-server-sdk-evaluation/v3"
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

	// GetPayloadFilter returns the environment's filter key, which may be an empty string indicating
	// default/unfiltered.
	GetPayloadFilter() config.FilterKey

	// SetIdentifiers updates the environment and project names and keys.
	SetIdentifiers(EnvIdentifiers)

	// GetSDKKey returns the anchor SDK key — the primary key that owns the upstream connection.
	// Use this when you need exactly the anchor (e.g. the status endpoint's sdkKey field) rather
	// than the full accepted set returned by GetCredentials.
	GetSDKKey() config.SDKKey

	// GetMobileKey returns the primary (default) mobile key. Like GetSDKKey for the anchor, use this
	// for the status endpoint's mobileKey field rather than iterating GetCredentials, which may return
	// several accepted mobile keys (primary + expiring) in nondeterministic order.
	GetMobileKey() config.MobileKey

	// GetAcceptedSDKKeys returns metadata for all non-anchor accepted SDK keys, sorted by identifier.
	// Used by the status endpoint to populate the sdkKeys[] array. Returns an empty slice for
	// single-key environments (those with only the anchor key).
	GetAcceptedSDKKeys() []credential.SDKKeyEntry

	// GetAcceptedMobileKeys returns metadata for all non-primary accepted mobile keys, sorted by
	// identifier. Used by the status endpoint to populate the mobileKeys[] array.
	GetAcceptedMobileKeys() []credential.MobileKeyEntry

	// GetEarliestExpiringSDKKey returns the non-anchor SDK key with the soonest expiry, and true.
	// Returns the zero SDKKey and false when no expiring non-anchor key exists. Used by the status
	// endpoint to populate expiringSdkKey deterministically.
	GetEarliestExpiringSDKKey() (config.SDKKey, bool)

	// ReconcileCredentials atomically reconciles the environment's accepted credentials to match
	// newSet. The set names its own anchor (the SDK key that owns the upstream connection) and
	// primary mobile key. The method owns the order of operations internally (add → re-anchor →
	// remove); callers do not sequence.
	//
	// newSet is assumed well-formed: it is built and validated via credential.AcceptedSetBuilder
	// (which guarantees an anchor) before reaching here, so this method does not re-validate.
	ReconcileCredentials(newSet credential.AcceptedSet)

	// GetCredentials returns all currently enabled and non-deprecated credentials for the environment.
	GetCredentials() []credential.SDKCredential

	// GetDeprecatedCredentials returns all deprecated and not-yet-removed credentials for the environment.
	GetDeprecatedCredentials() []credential.SDKCredential

	// GetClient returns the SDK client instance for this environment. This is nil if initialization is not yet
	// complete. Rather than providing the full client object, we use the simpler sdks.LDClientContext which
	// includes only the operations Relay needs to do.
	GetClient() sdks.LDClientContext

	// GetStore returns the SDK data store instance for this environment. This is nil if initialization is not
	// yet complete.
	GetStore() subsystems.DataStore

	// GetEvaluator returns an instance of the evaluation engine for evaluating feature flags in this environment.
	// This is nil if initialization is not yet complete.
	GetEvaluator() ldeval.Evaluator

	// GetBigSegmentStore returns the big segment data store instance for this environment. If a big
	// segment store is not configured this returns nil.
	GetBigSegmentStore() bigsegments.BigSegmentStore

	// GetLoggers returns a Loggers instance that is specific to this environment. We configure each of these to
	// have its own prefix string and, optionally, its own log level.
	GetLoggers() ldlog.Loggers

	// GetStreamHandler returns the HTTP handler for the specified kind of stream requests and credential for this
	// environment. If there is none, it returns a handler for a 404 status (not nil).
	GetStreamHandler(streams.StreamProvider, credential.SDKCredential) http.Handler

	// GetEventDispatcher returns the object that proxies events for this environment.
	GetEventDispatcher() *events.EventDispatcher

	// GetJSClientContext returns the JSClientContext that is used for browser endpoints.
	GetJSClientContext() JSClientContext

	// GetMetricsContext returns the Context that should be used for OpenCensus operations related to this
	// environment.
	GetMetricsContext() context.Context

	// GetMetricsManager returns the top-level object that controls all of our metrics exporter activity.
	GetMetricsManager() *metrics.Manager

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

	// FilterKey is the environment's payload filter. Empty string indicates no filter.
	FilterKey config.FilterKey

	// ConfiguredName is a human-readable unique name for this environment, if the user specified one. When
	// using a local configuration, this is always set; in auto-configuration mode, it is always empty (but
	// EnvIdentifiers.GetDisplayName() will compute one).
	ConfiguredName string
}

// GetDisplayName returns a human-readable unique name for this environment. If none was set in the
// configuration, it computes one in the format "ProjName EnvName".
func (ei EnvIdentifiers) GetDisplayName() string {
	if ei.ConfiguredName == "" {
		if ei.FilterKey != "" {
			return fmt.Sprintf("%s %s (%s)", ei.ProjName, ei.EnvName, ei.FilterKey)
		}
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
