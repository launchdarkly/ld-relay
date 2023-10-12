// Package sdkauth represents the authentication parameters that an SDK provides to Relay in order to gain
// access to an environment.
//
// Before the introduction of the Payload Filtering feature, authentication could be solely a SDKCredential. These
// credentials were valid for accessing all the flags/data in an environment. For example, a downstream Server-Side SDK
// could present RP with a server-side SDK key, which would allow the SDK to receive all the corresponding environment's
// flag/segments.
//
// After the introduction of Payload Filtering, it is no longer possible to uniquely identify an environment using an
// SDKCredential. Instead, an SDKCredential must be paired with a FilterKey; the combination of both is a ScopedCredential.
//
// A ScopedCredential may have the default scope (that is, an empty filter key), meaning it is equivalent to the scheme
// laid out in the first paragraph. Such an SDK can access all flags/segments in the corresponding environment.
// Otherwise, it will have a non-empty filter key, meaning it is limited to the data configured by that filter key.
package sdkauth

import (
	"fmt"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
)

// ScopedCredential scopes an SDKCredential to a filtered environment identified by FilterKey.
type ScopedCredential struct {
	credential.SDKCredential
	config.FilterKey
}

// New wraps an existing SDKCredential, returning a ScopedCredential which has the default scope - full access
// to all environment data (meaning no payload filter).
func New(sdkCredential credential.SDKCredential) ScopedCredential {
	return ScopedCredential{SDKCredential: sdkCredential, FilterKey: config.DefaultFilter}
}

// NewScoped wraps an existing SDKCredential, returning a ScopedCredential which is valid only for the filtered environment
// named by filterKey.
func NewScoped(filterKey config.FilterKey, sdkCredential credential.SDKCredential) ScopedCredential {
	return ScopedCredential{SDKCredential: sdkCredential, FilterKey: filterKey}
}

// String returns the string representation of the ScopedCredential.
// This is useful for any code that must maintain a map lookup keyed on a ScopedCredential.
// If the scope is default, then the string form is that of the underlying credential.
// Otherwise, it is the concatenation of the credential's string form and the filter, separated by a '/'.
// The delimiter ('/') must be a character that is not a valid credential or filter key character.
func (p ScopedCredential) String() string {
	if p.FilterKey == config.DefaultFilter {
		return p.SDKCredential.String()
	}
	return fmt.Sprintf("%s/%s", p.SDKCredential, p.FilterKey)
}

// Unscope removes any scope associated with a ScopedCredential.
func (p ScopedCredential) Unscope() ScopedCredential {
	return ScopedCredential{SDKCredential: p.SDKCredential}
}
