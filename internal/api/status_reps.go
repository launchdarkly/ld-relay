package api

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
)

// The values the status document reports for its state fields. They live here, rather than only
// where the document is built, because the status metrics report the same vocabulary as attribute
// values and the two must agree.
const (
	// StatusHealthy means Relay knows its full set of environments and every one of them is
	// healthy.
	StatusHealthy = "healthy"

	// StatusDegraded means at least one environment is unhealthy, or Relay does not yet know its
	// full set of environments. Relay keeps serving flags in this state.
	StatusDegraded = "degraded"

	// EnvStatusConnected means the environment has a client that is initialized and whose data
	// source has not been disconnected for longer than the configured grace period.
	EnvStatusConnected = "connected"

	// EnvStatusDisconnected means the environment has no client yet, or its data source has been
	// away for longer than the configured grace period. The environment still serves the data it
	// has.
	EnvStatusDisconnected = "disconnected"

	// The data store states. These spell their values the same way DataSourceState does, but the
	// data store status is a plain string in the document rather than that type, and it never
	// reports OFF.
	DataStoreStateValid        = "VALID"
	DataStoreStateInitializing = "INITIALIZING"
	DataStoreStateInterrupted  = "INTERRUPTED"
)

// StatusRep is the JSON representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type StatusRep struct {
	Environments     map[string]EnvironmentStatusRep `json:"environments"`
	AutoConfigStatus *AutoConfigStatusRep            `json:"autoConfigStatus,omitempty"`
	Status           string                          `json:"status"`
	Version          string                          `json:"version"`
	ClientVersion    string                          `json:"clientVersion"`
}

// AutoConfigStatusRep is the status of the auto-configuration stream. It is present only when
// Relay runs in automatic configuration mode.
//
// This is exported for use in integration test code.
type AutoConfigStatusRep struct {
	State      interfaces.DataSourceState `json:"state"`
	StateSince ldtime.UnixMillisecondTime `json:"stateSince"`
	LastError  *ConnectionErrorRep        `json:"lastError,omitempty"`
}

// EnvironmentStatusRep is the per-environment JSON representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type EnvironmentStatusRep struct {
	// SDKKey is the obscured anchor SDK key. It designates which SDKKeys entry owns the
	// environment's connection to LaunchDarkly.
	//
	// It is deliberately kept alongside the array rather than replaced by a flag on an entry. It
	// predates concurrent keys and is what consumers read to identify an environment, v8 reports the
	// same pair of fields, and encoding the designation in two places would let them drift.
	SDKKey string `json:"sdkKey"`
	// SDKKeys is every server-side SDK key the environment accepts, the anchor included. It is always
	// present and always holds at least the anchor. Order is unspecified.
	SDKKeys []KeyStatus `json:"sdkKeys"`
	EnvID   string      `json:"envId,omitempty"`
	EnvKey  string      `json:"envKey,omitempty"`
	EnvName string      `json:"envName,omitempty"`
	ProjKey string      `json:"projKey,omitempty"`
	// ProjName is the project's name.
	ProjName string `json:"projName,omitempty"`
	// MobileKey is the obscured primary mobile key. It designates which MobileKeys entry is the one
	// used where a single mobile key is required, such as event forwarding.
	MobileKey string `json:"mobileKey,omitempty"`
	// MobileKeys is every mobile key the environment accepts, the primary included. It is always
	// present, and empty for an environment with no mobile key.
	MobileKeys       []KeyStatus          `json:"mobileKeys"`
	Status           string               `json:"status"`
	ConnectionStatus ConnectionStatusRep  `json:"connectionStatus"`
	DataStoreStatus  DataStoreStatusRep   `json:"dataStoreStatus"`
	BigSegmentStatus *BigSegmentStatusRep `json:"bigSegmentStatus,omitempty"`
}

// KeyStatus is one accepted credential in the status resource's sdkKeys and mobileKeys arrays.
//
// Key is the non-secret wire identifier, omitted when the source carried none. Value is the
// credential secret, obscured. Expiry is a Unix millisecond timestamp, omitted for a permanent key.
//
// This replaces the singular expiringSdkKey field, which could name only one key and so could not
// describe an environment that accepts several with different expiries.
type KeyStatus struct {
	Key    string `json:"key,omitempty"`
	Value  string `json:"value"`
	Expiry *int64 `json:"expiry,omitempty"`
}

// BigSegmentStatusRep is the big segment status representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type BigSegmentStatusRep struct {
	Available          bool                       `json:"available"`
	PotentiallyStale   bool                       `json:"potentiallyStale"`
	LastSynchronizedOn ldtime.UnixMillisecondTime `json:"lastSynchronizedOn"`
}

// ConnectionStatusRep is the data source status representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type ConnectionStatusRep struct {
	State      interfaces.DataSourceState `json:"state"`
	StateSince ldtime.UnixMillisecondTime `json:"stateSince"`
	LastError  *ConnectionErrorRep        `json:"lastError,omitempty"`
}

// ConnectionErrorRep is the optional error information in ConnectionStatusRep.
//
// This is exported for use in integration test code.
type ConnectionErrorRep struct {
	Kind interfaces.DataSourceErrorKind `json:"kind"`
	// StatusCode is the HTTP status that caused the error. It is absent for an error that did not
	// come from an HTTP response, such as a network failure.
	StatusCode int                        `json:"statusCode,omitempty"`
	Time       ldtime.UnixMillisecondTime `json:"time"`
}

// DataStoreStatusRep is the data store status representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type DataStoreStatusRep struct {
	State      string                     `json:"state"`
	StateSince ldtime.UnixMillisecondTime `json:"stateSince"`
	Database   string                     `json:"database,omitempty"`
	DBServer   string                     `json:"dbServer,omitempty"`
	DBPrefix   string                     `json:"dbPrefix,omitempty"`
	DBTable    string                     `json:"dbTable,omitempty"`
}
