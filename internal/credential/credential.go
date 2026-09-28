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
	// Masked returns a masked form of the credential suitable for log messages.
	Masked() string
}
