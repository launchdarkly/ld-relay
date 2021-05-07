package basictypes

// SDKKind represents any of the supported SDK categories that has distinct behavior from the others.
type SDKKind string

const (
	// ServerSDK represents server-side SDKs, which use server-side endpoints and authenticate their requests
	// with an SDK key.
	ServerSDK SDKKind = "server"

	// MobileSDK represents mobile SDKs, which use mobile endpoints and authenticate their requests with a
	// mobile key.
	MobileSDK SDKKind = "mobile"

	// JSClientSDK represents client-side JavaScript-based SDKs, which use client-side endpoints and
	// authenticate their requests insecurely with an environment ID.
	JSClientSDK SDKKind = "js"
)
