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

// AcceptedSet is the full set of credentials that an environment should accept after a
// reconcile. It carries every accepted server-side SDK key (each with an optional expiry) and
// mobile key, plus the single environment ID. The anchor (the one SDK key that owns the
// environment's upstream connection) is supplied separately to ReconcileCredentials.
//
// A key's expiry is taken from its entry in this set; the legacy sdkKey.expiring{} wire slot is
// not consulted when building it.
//
// Build an AcceptedSet with NewAcceptedSet and the With* methods, which ignore undefined
// credentials so callers can supply optional keys unconditionally.
type AcceptedSet struct {
	sdkKeys    []acceptedSDKKey
	mobileKeys []config.MobileKey
	envID      config.EnvironmentID
}

type acceptedSDKKey struct {
	key    config.SDKKey
	expiry *time.Time // nil = permanent
}

// NewAcceptedSet returns an empty AcceptedSet.
func NewAcceptedSet() AcceptedSet {
	return AcceptedSet{}
}

// WithSDKKey adds a permanent (non-expiring) SDK key to the set. It is a no-op if the key is
// undefined.
func (s AcceptedSet) WithSDKKey(key config.SDKKey) AcceptedSet {
	if key.Defined() {
		s.sdkKeys = append(s.sdkKeys, acceptedSDKKey{key: key})
	}
	return s
}

// WithExpiringSDKKey adds an SDK key that should be accepted until the given expiry. It is a
// no-op if the key is undefined.
func (s AcceptedSet) WithExpiringSDKKey(key config.SDKKey, expiry time.Time) AcceptedSet {
	if key.Defined() {
		exp := expiry
		s.sdkKeys = append(s.sdkKeys, acceptedSDKKey{key: key, expiry: &exp})
	}
	return s
}

// WithMobileKey adds a mobile key to the set. It is a no-op if the key is undefined.
func (s AcceptedSet) WithMobileKey(key config.MobileKey) AcceptedSet {
	if key.Defined() {
		s.mobileKeys = append(s.mobileKeys, key)
	}
	return s
}

// WithEnvironmentID sets the environment ID for the set. It is a no-op if the ID is undefined.
func (s AcceptedSet) WithEnvironmentID(id config.EnvironmentID) AcceptedSet {
	if id.Defined() {
		s.envID = id
	}
	return s
}

// hasSDKKey reports whether key is one of the set's accepted SDK keys.
func (s AcceptedSet) hasSDKKey(key config.SDKKey) bool {
	for _, k := range s.sdkKeys {
		if k.key == key {
			return true
		}
	}
	return false
}

// deprecatedSDKKey returns the accepted SDK key other than the anchor that carries an expiry —
// the key being phased out with a grace period. There is at most one such key today.
func (s AcceptedSet) deprecatedSDKKey(anchor config.SDKKey) (acceptedSDKKey, bool) {
	for _, k := range s.sdkKeys {
		if k.key != anchor && k.expiry != nil {
			return k, true
		}
	}
	return acceptedSDKKey{}, false
}

// primaryMobileKey returns the environment's single accepted mobile key, or false if there is none.
func (s AcceptedSet) primaryMobileKey() (config.MobileKey, bool) {
	if len(s.mobileKeys) > 0 {
		return s.mobileKeys[0], true
	}
	return "", false
}

// MalformedCredentialSetError is returned by ReconcileCredentials when the supplied anchor SDK
// key is not present among the accepted set's SDK keys — a violation of the backend invariant
// that the anchor (sdkKey.value) always appears in sdkKeys[].
//
// When ReconcileCredentials returns this error it has made no changes, so the environment's
// previous accepted set is preserved. The caller is responsible for the second half of the
// malformed-payload policy: reconnecting the RAC stream with jitter to force a fresh put. RAC is
// one-way push with no NAK channel, so without the reconnect the backend would believe the
// malformed patch was applied and would not send fresh state.
type MalformedCredentialSetError struct {
	// Anchor is the anchor credential that was not found among the set's SDK keys.
	Anchor credential.SDKCredential
}

func (e *MalformedCredentialSetError) Error() string {
	if e.Anchor == nil {
		return "malformed credential set: anchor SDK key is missing"
	}
	return fmt.Sprintf("malformed credential set: anchor SDK key %s is not present in the accepted set",
		e.Anchor.Masked())
}

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

	// ReconcileCredentials atomically reconciles the environment's accepted credentials to match
	// newSet, with anchor designating the SDK key that owns the upstream connection. The method
	// owns the order of operations internally (add → re-anchor → remove); callers do not sequence.
	//
	// It returns a *MalformedCredentialSetError, without changing any state, if anchor is not one
	// of newSet's SDK keys; see that type for the caller's responsibilities.
	ReconcileCredentials(newSet AcceptedSet, anchor credential.SDKCredential) error

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
