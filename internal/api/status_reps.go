package api

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
)

// StatusRep is the JSON representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type StatusRep struct {
	Environments  map[string]EnvironmentStatusRep `json:"environments"`
	Status        string                          `json:"status"`
	Version       string                          `json:"version"`
	ClientVersion string                          `json:"clientVersion"`
}

// KeyStatus is the JSON representation of one accepted SDK or mobile key in the status endpoint's
// sdkKeys[] / mobileKeys[] arrays.
//
// Key is the non-secret human-readable identifier from the wire format (the "key" field of a
// sdkKeys/mobileKeys entry); it is omitted when the source carried no identifier (manual config, or
// an old-format payload predating concurrent keys). Value is the obscured credential secret (via
// sdks.ObscureKey). Expiry carries the Unix-millisecond expiry timestamp when the key is being phased
// out; it is omitted for permanent keys.
type KeyStatus struct {
	Key    string `json:"key,omitempty"`
	Value  string `json:"value"`
	Expiry *int64 `json:"expiry,omitempty"`
}

// EnvironmentStatusRep is the per-environment JSON representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type EnvironmentStatusRep struct {
	// SDKKey is the obscured anchor SDK key — the key relay uses for its upstream connection. It
	// designates which SDKKeys entry is the anchor.
	SDKKey string `json:"sdkKey"`
	// SDKKeys carries the full accepted set of server-side SDK keys — including the anchor — with their
	// identifiers, obscured values, and optional expiry. Always present; always contains at least the
	// anchor.
	SDKKeys  []KeyStatus `json:"sdkKeys"`
	EnvID    string      `json:"envId,omitempty"`
	EnvKey   string      `json:"envKey,omitempty"`
	EnvName  string      `json:"envName,omitempty"`
	ProjKey  string      `json:"projKey,omitempty"`
	ProjName string      `json:"projName,omitempty"`
	// MobileKey is the obscured primary mobile key. It designates which MobileKeys entry is the primary.
	MobileKey string `json:"mobileKey,omitempty"`
	// MobileKeys carries the full accepted set of mobile keys — including the primary. Always present;
	// empty for an environment with no mobile keys (e.g. server-side only).
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
	Time ldtime.UnixMillisecondTime     `json:"time"`
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
