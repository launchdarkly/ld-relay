// Package credential defines the main SDKCredential interface used throughout the codebase,
// as well as a means to detect how a credential has changed.
package credential

// SDKCredential is implemented by types that represent an SDK authorization credential (SDKKey, etc.).
type SDKCredential interface {
	// GetAuthorizationHeaderValue returns the value that should be passed in an HTTP Authorization header
	// when using this credential, or "" if the header is not used.
	GetAuthorizationHeaderValue() string
	// Defined returns true if the credential is present.
	Defined() bool
	// String returns the string form of the credential.
	String() string
	// Compare accepts a collection of AutoConfig credentials and inspects it, determining if this credential has
	// changed in any way. If so, it should return the new credential and a status.
	Compare(creds AutoConfig) (SDKCredential, Status)

	// Masked returns a masked form of the credential suitable for log messages.
	Masked() string
}

// Status represents that difference between an existing credential and one found in a new AutoConfig configuration
// struct.
type Status string

const (
	// Unchanged means the credential has not changed.
	Unchanged = Status("unchanged")
	// Deprecated means the existing credential has been deprecated in favor of a new one.
	Deprecated = Status("deprecated")
	// Expired means the existing credential should be removed in favor of a new one.
	Expired = Status("expired")
)

// AutoConfig represents credentials that are updated via AutoConfig protocol.
type AutoConfig struct {
	// SDKKey is the environment's SDK key; if there is more than one active key, it is the latest.
	SDKKey SDKCredential
	// ExpiringSDKKey is an additional SDK key that may or may not be present; it represents the fact that a deprecated
	// key may exist which can still authenticate a given connection.
	ExpiringSDKKey SDKCredential
	// MobileKey is the environment's mobile key.
	MobileKey SDKCredential
}
