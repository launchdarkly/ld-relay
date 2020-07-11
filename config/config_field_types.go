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
type OptAbsoluteURL struct {
	url *url.URL
}

func NewOptAbsoluteURLFromURL(url *url.URL) (OptAbsoluteURL, error) {
	if url == nil {
		return OptAbsoluteURL{}, nil
	}
	if !url.IsAbs() {
		return OptAbsoluteURL{}, errors.New("must be an absolute URL/URI")
	}
	u := *url
	return OptAbsoluteURL{url: &u}, nil
}

func NewOptAbsoluteURLFromString(urlString string) (OptAbsoluteURL, error) {
	u, err := url.Parse(urlString)
	if err == nil {
		return NewOptAbsoluteURLFromURL(u)
	}
	return OptAbsoluteURL{}, errors.New("not a valid URL/URI")
}

func newOptAbsoluteURLMustBeValid(urlString string) OptAbsoluteURL {
	o, err := NewOptAbsoluteURLFromString(urlString)
	if err != nil {
		panic(err)
	}
	return o
}

func (o OptAbsoluteURL) IsDefined() bool {
	return o.url != nil
}

func (o OptAbsoluteURL) Get() *url.URL {
	if o.url == nil {
		return nil
	}
	u := *o.url // copy the value so we're not exposing anything mutable
	return &u
}

func (o OptAbsoluteURL) String() string {
	return o.StringOrElse("")
}

func (o OptAbsoluteURL) StringOrElse(orElseValue string) string {
	if o.url == nil {
		return orElseValue
	}
	return o.url.String()
}

func (o *OptAbsoluteURL) UnmarshalText(data []byte) error {
	if len(data) == 0 {
		o.url = nil
		return nil
	}
	parsed, err := NewOptAbsoluteURLFromString(string(data))
	if err == nil {
		*o = parsed
	}
	return err
}

// OptLogLevel represents an optional log level parameter. It must match one of the level names "debug",
// "info", "warn", or "error" (case-insensitive).
type OptLogLevel struct {
	level ldlog.LogLevel
}

func NewOptLogLevel(level ldlog.LogLevel) OptLogLevel {
	return OptLogLevel{level: level}
}

func NewOptLogLevelFromString(levelName string) (OptLogLevel, error) {
	if levelName == "" {
		return OptLogLevel{}, nil
	}
	for _, level := range []ldlog.LogLevel{ldlog.Debug, ldlog.Info, ldlog.Warn, ldlog.Error, ldlog.None} {
		if strings.EqualFold(level.Name(), levelName) {
			return NewOptLogLevel(level), nil
		}
	}
	return OptLogLevel{}, fmt.Errorf(`"%s" is not a valid log level`, levelName)
}

func (o OptLogLevel) IsDefined() bool {
	return o.level != 0
}

func (o OptLogLevel) GetOrElse(orElseValue ldlog.LogLevel) ldlog.LogLevel {
	if o.level == 0 {
		return orElseValue
	}
	return o.level
}

func (o *OptLogLevel) UnmarshalText(data []byte) error {
	opt, err := NewOptLogLevelFromString(string(data))
	if err == nil {
		*o = opt
	}
	return err
}
