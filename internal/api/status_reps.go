package api

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
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
// It uses the same types as the per-environment connectionStatus, so the two report the same
// states and error kinds. A state other than VALID means Relay is no longer learning about
// environment changes, even though the environments it already knows about keep serving flags.
//
// This is exported for use in integration test code.
type AutoConfigStatusRep struct {
	State      interfaces.DataSourceState `json:"state"`
	StateSince ldtime.UnixMillisecondTime `json:"stateSince"`
	LastError  *ConnectionErrorRep        `json:"lastError,omitempty"`
}

// KeyStatus is the JSON representation of one accepted SDK or mobile key in the status endpoint's
// sdkKeys[] and mobileKeys[] arrays.
//
// Key is the non-secret wire identifier, omitted when the source carried none. Value is the credential
// secret, obscured by sdks.ObscureKey. Expiry is the expiry timestamp in Unix milliseconds, omitted
// for permanent keys.
type KeyStatus struct {
	Key    string `json:"key,omitempty"`
	Value  string `json:"value"`
	Expiry *int64 `json:"expiry,omitempty"`
}

// EnvironmentStatusRep is the per-environment JSON representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type EnvironmentStatusRep struct {
	// SDKKey is the obscured anchor SDK key. It designates which SDKKeys entry is the anchor.
	SDKKey string `json:"sdkKey"`
	// SDKKeys carries the full accepted set of server-side SDK keys, including the anchor. It is always
	// present and always contains at least the anchor.
	SDKKeys  []KeyStatus `json:"sdkKeys"`
	EnvID    string      `json:"envId,omitempty"`
	EnvKey   string      `json:"envKey,omitempty"`
	EnvName  string      `json:"envName,omitempty"`
	ProjKey  string      `json:"projKey,omitempty"`
	ProjName string      `json:"projName,omitempty"`
	// MobileKey is the obscured primary mobile key. It designates which MobileKeys entry is the primary.
	MobileKey string `json:"mobileKey,omitempty"`
	// MobileKeys carries the full accepted set of mobile keys, including the primary. It is always
	// present, and empty for an environment with no mobile keys.
	MobileKeys       []KeyStatus          `json:"mobileKeys"`
	ExpiringSDKKey   string               `json:"expiringSdkKey,omitempty"`
	Status           string               `json:"status"`
	ConnectionStatus ConnectionStatusRep  `json:"connectionStatus"`
	DataStoreStatus  DataStoreStatusRep   `json:"dataStoreStatus"`
	BigSegmentStatus *BigSegmentStatusRep `json:"bigSegmentStatus,omitempty"`
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
