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

// EnvironmentStatusRep is the per-environment JSON representation returned by the status endpoint.
//
// This is exported for use in integration test code.
type EnvironmentStatusRep struct {
	SDKKey           string               `json:"sdkKey"`
	EnvID            string               `json:"envId,omitempty"`
	EnvKey           string               `json:"envKey,omitempty"`
	EnvName          string               `json:"envName,omitempty"`
	ProjKey          string               `json:"projKey,omitempty"`
	ProjName         string               `json:"projName,omitempty"`
	MobileKey        string               `json:"mobileKey,omitempty"`
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
