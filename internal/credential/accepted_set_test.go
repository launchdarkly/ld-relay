package credential

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
)

func TestMalformedCredentialSetErrorMessage(t *testing.T) {
	// A nil anchor reports "missing" rather than dereferencing a nil credential.
	assert.Equal(t, "malformed credential set: anchor SDK key is missing",
		(&MalformedCredentialSetError{Anchor: nil}).Error())

	// A defined anchor is masked in the message.
	assert.Contains(t, (&MalformedCredentialSetError{Anchor: config.SDKKey("sdk-abcd1234")}).Error(),
		"...1234")
}
