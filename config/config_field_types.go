package config

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

func errBadLogLevel(s string) error {
	return fmt.Errorf("%q is not a valid log level", s)
}

func errBadTLSVersion(s string) error {
	return fmt.Errorf("%q is not a valid TLS version", s)
}

// SDKKey is a type tag to indicate when a string is used as a server-side SDK key for a LaunchDarkly
// environment.
type SDKKey string

// MobileKey is a type tag to indicate when a string is used as a mobile key for a LaunchDarkly
// environment.
type MobileKey string

// EnvironmentID is a type tag to indicate when a string is used as a client-side environment ID for a
// LaunchDarkly environment.
type EnvironmentID string

// AutoConfigKey is a type tag to indicate when a string is used as an auto-configuration key.
type AutoConfigKey string

// FilterID represents the unique ID for a filter. It is different from the key, which is scoped to the project
// level.
type FilterID string

// FilterKey represents the key that should be used when making requests to LaunchDarkly in order to obtain
// a filtered environment.
type FilterKey string

// DefaultFilter represents the lack of a filter, meaning a full LaunchDarkly environment.
const DefaultFilter = FilterKey("")

func last4Chars(s string) string {
	if len(s) < 4 { // COVERAGE: doesn't happen in unit tests, also can't happen with real environments
		return s
	}
	return s[len(s)-4:]
}

// GetAuthorizationHeaderValue for SDKKey returns the same string, since SDK keys are passed in
// the Authorization header.
func (k SDKKey) GetAuthorizationHeaderValue() string {
	return string(k)
}

func (k SDKKey) Defined() bool {
	return k != ""
}

func (k SDKKey) String() string {
	return string(k)
}
func (k SDKKey) Masked() string { return "..." + last4Chars(k.String()) }

// GetAuthorizationHeaderValue for MobileKey returns the same string, since mobile keys are passed in the
// Authorization header.
func (k MobileKey) GetAuthorizationHeaderValue() string {
	return string(k)
}

func (k MobileKey) Defined() bool {
	return k != ""
}

func (k MobileKey) String() string {
	return string(k)
}

func (k MobileKey) Masked() string { return "..." + last4Chars(k.String()) }

// GetAuthorizationHeaderValue for EnvironmentID returns an empty string, since environment IDs are not
// passed in a header but as part of the request URL.
func (k EnvironmentID) GetAuthorizationHeaderValue() string {
	return ""
}

func (k EnvironmentID) Defined() bool {
	return k != ""
}

func (k EnvironmentID) String() string {
	return string(k)
}

// Masked is an alias for String(), because EnvironmentIDs are considered non-sensitive public information.
func (k EnvironmentID) Masked() string { return k.String() }

// GetAuthorizationHeaderValue for AutoConfigKey returns the same string, since these keys are passed in
// the Authorization header. Note that unlike the other kinds of authorization keys, this one is never
// present in an incoming request; it is only used in requests from Relay to LaunchDarkly.
func (k AutoConfigKey) GetAuthorizationHeaderValue() string {
	return string(k)
}

func (k AutoConfigKey) String() string {
	return string(k)
}

func (k AutoConfigKey) Masked() string { return last4Chars(string(k)) }

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

// UnmarshalText allows the AutoConfigKey type to be set from environment variables.
func (k *AutoConfigKey) UnmarshalText(data []byte) error {
	*k = AutoConfigKey(string(data))
	return nil
}

func (k AutoConfigKey) Defined() bool {
	return k != ""
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

// OptTLSVersion represents an optional TLS level parameter. When represented as a string, it must be
// "1.0", "1.1", "1.2", or "1.3". This is converted into a uint16 value as defined by crypto/tls.
type OptTLSVersion struct {
	value uint16
}

// NewOptTLSVersion creates an OptTLSVersion that wraps the given value. It does not validate that the
// value is one supported by crypto/tls. A value of zero is equivalent to undefined.
func NewOptTLSVersion(value uint16) OptTLSVersion {
	return OptTLSVersion{value}
}

// NewOptTLSVersionFromString creates an OptTLSVersion corresponding to the given version string, which must
// be either a valid TLS major and minor version ("1.2") or an empty string.
func NewOptTLSVersionFromString(version string) (OptTLSVersion, error) {
	switch version {
	case "":
		return NewOptTLSVersion(0), nil
	case "1.0":
		return NewOptTLSVersion(tls.VersionTLS10), nil
	case "1.1":
		return NewOptTLSVersion(tls.VersionTLS11), nil
	case "1.2":
		return NewOptTLSVersion(tls.VersionTLS12), nil
	case "1.3":
		return NewOptTLSVersion(tls.VersionTLS13), nil
	default:
		return OptTLSVersion{}, errBadTLSVersion(version)
	}
}

// IsDefined returns true if the instance contains a value.
func (o OptTLSVersion) IsDefined() bool {
	return o.value != 0
}

// Get returns the wrapped value, or zero if there is no value.
func (o OptTLSVersion) Get() uint16 {
	return o.value
}

// UnmarshalText attempts to parse the value from a byte string, using the same logic as
// NewOptTLSVersionFromString.
func (o *OptTLSVersion) UnmarshalText(data []byte) error {
	opt, err := NewOptTLSVersionFromString(string(data))
	if err == nil {
		*o = opt
	}
	return err
}

// String returns a string description of the value.
func (o OptTLSVersion) String() string {
	switch o.value {
	case 0:
		return ""
	case tls.VersionTLS10:
		return "1.0"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS13:
		return "1.3"
	default:
		return fmt.Sprintf("unknown (%d)", o.value)
	}
}
