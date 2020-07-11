package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

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

// OptAbsoluteURL represents an optional URL parameter which, if present, must be a valid URL. This is enforced
// by its representation of encoding.TextUnmarshaler, which is called by both the gcfg config file parser
// and our environment variable logic.
//
// The zero value OptAbsoluteURL{} is valid and undefined (IsDefined() is false, Get() is nil).
type OptAbsoluteURL struct {
	url *url.URL
}

// NewOptAbsoluteURLFromURL creates an OptAbsoluteURL from an already-parsed URL. It returns an error if the
// URL is not an absolute URL. If the parameter is nil, it returns an empty OptAbsoluteURL{}.
func NewOptAbsoluteURLFromURL(url *url.URL) (OptAbsoluteURL, error) {
	if url == nil {
		return OptAbsoluteURL{}, nil
	}
	if !url.IsAbs() {
		return OptAbsoluteURL{}, errNotAbsoluteURL()
	}
	u := *url
	return OptAbsoluteURL{url: &u}, nil
}

// NewOptAbsoluteURLFromURL creates an OptAbsoluteURL from a string. It returns an error if the string is not
// a URL or is a relative URL. If the string is empty, it returns an empty OptAbsoluteURL{}.
func NewOptAbsoluteURLFromString(urlString string) (OptAbsoluteURL, error) {
	if urlString == "" {
		return OptAbsoluteURL{}, nil
	}
	u, err := url.Parse(urlString)
	if err == nil {
		return NewOptAbsoluteURLFromURL(u)
	}
	return OptAbsoluteURL{}, errBadURLString()
}

func newOptAbsoluteURLMustBeValid(urlString string) OptAbsoluteURL {
	o, err := NewOptAbsoluteURLFromString(urlString)
	if err != nil {
		panic(err)
	}
	return o
}

// IsDefined is true if this instance has a value (Get() is not nil).
func (o OptAbsoluteURL) IsDefined() bool {
	return o.url != nil
}

// Get returns the wrapped URL if any, or nil.
func (o OptAbsoluteURL) Get() *url.URL {
	if o.url == nil {
		return nil
	}
	u := *o.url // copy the value so we're not exposing anything mutable
	return &u
}

// String returns the URL converted to a string, or "" if undefined.
func (o OptAbsoluteURL) String() string {
	return o.StringOrElse("")
}

// StringOrElse returns the URL converted to a string, or the alternative value if undefined.
func (o OptAbsoluteURL) StringOrElse(orElseValue string) string {
	if o.url == nil {
		return orElseValue
	}
	return o.url.String()
}

// UnmarshalText attempts to parse the value from a byte string, using the same logic as
// NewOptAbsoluteURLFromString.
func (o *OptAbsoluteURL) UnmarshalText(data []byte) error {
	parsed, err := NewOptAbsoluteURLFromString(string(data))
	if err == nil {
		*o = parsed
	}
	return err
}

func errBadURLString() error {
	return errors.New("not a valid URL/URI")
}

func errNotAbsoluteURL() error {
	return errors.New("must be an absolute URL/URI")
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
