package config

import (
	"fmt"
	"strings"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// SDKKey is a type tag to indicate when a string is used as a server-side SDK key for a LaunchDarkly
// environment.
type SDKKey string

// MobileKey is a type tag to indicate when a string is used as a mobile key for a LaunchDarkly
// environment.
type MobileKey string

// EnvironmentID is a type tag to indicate when a string is used as a client-side environment ID for a
// LaunchDarkly environment.
type EnvironmentID string

// SDKCredential is implemented by types that represent an SDK authorization credential (SDKKey, etc.)
type SDKCredential interface {
	// GetAuthorizationHeaderValue returns the value that should be passed in an HTTP Authorization header
	// when using this credential, or "" if the header is not used.
	GetAuthorizationHeaderValue() string
}

// GetAuthorizationHeaderValue for SDKKey returns the same string, since SDK keys are passed in
// the Authorization header.
func (k SDKKey) GetAuthorizationHeaderValue() string {
	return string(k)
}

// GetAuthorizationHeaderValue for MobileKey returns the same string, since mobile keys are passed in the
// Authorization header.
func (k MobileKey) GetAuthorizationHeaderValue() string {
	return string(k)
}

// GetAuthorizationHeaderValue for EnvironmentID returns an empty string, since environment IDs are not
// passed in a header but as part of the request URL.
func (k EnvironmentID) GetAuthorizationHeaderValue() string {
	return ""
}

// UnmarshalText allows the SDKKey type to be set from environment variables.
func (k *SDKKey) UnmarshalText(data []byte) error {
	*k = SDKKey(string(data))
	return nil
}

// UnmarshalText allows the MobileKey type to be set from environment variables.
func (k *MobileKey) UnmarshalText(data []byte) error {
	*k = MobileKey(string(data))
	return nil
}

// UnmarshalText allows the EnvironmentID type to be set from environment variables.
func (k *EnvironmentID) UnmarshalText(data []byte) error {
	*k = EnvironmentID(string(data))
	return nil
}

func newOptURLAbsoluteMustBeValid(urlString string) ct.OptURLAbsolute {
	o, err := ct.NewOptURLAbsoluteFromString(urlString)
	if err != nil {
		panic(err)
	}
	return o
}

// OptLogLevel represents an optional log level parameter. It must match one of the level names "debug",
// "info", "warn", or "error" (case-insensitive).
//
// The zero value OptLogLevel{} is valid and undefined (IsDefined() is false).
type OptLogLevel struct {
	level ldlog.LogLevel
}

// NewOptLogLevel creates an OptLogLevel that wraps the given value.
func NewOptLogLevel(level ldlog.LogLevel) OptLogLevel {
	return OptLogLevel{level: level}
}

// NewOptLogLevelFromString creates an OptLogLevel from a string that must either be a valid log level
// name or an empty string.
func NewOptLogLevelFromString(levelName string) (OptLogLevel, error) {
	if levelName == "" {
		return OptLogLevel{}, nil
	}
	for _, level := range []ldlog.LogLevel{ldlog.Debug, ldlog.Info, ldlog.Warn, ldlog.Error, ldlog.None} {
		if strings.EqualFold(level.Name(), levelName) {
			return NewOptLogLevel(level), nil
		}
	}
	return OptLogLevel{}, errBadLogLevel(levelName)
}

// IsDefined returns true if the instance contains a value.
func (o OptLogLevel) IsDefined() bool {
	return o.level != 0
}

// GetOrElse returns the wrapped value, or the alternative value if there is no value.
func (o OptLogLevel) GetOrElse(orElseValue ldlog.LogLevel) ldlog.LogLevel {
	if o.level == 0 {
		return orElseValue
	}
	return o.level
}

// UnmarshalText attempts to parse the value from a byte string, using the same logic as
// NewOptLogLevelFromString.
func (o *OptLogLevel) UnmarshalText(data []byte) error {
	opt, err := NewOptLogLevelFromString(string(data))
	if err == nil {
		*o = opt
	}
	return err
}

func errBadLogLevel(s string) error {
	return fmt.Errorf("%q is not a valid log level", s)
}
