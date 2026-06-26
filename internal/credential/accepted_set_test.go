package credential

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
)

func TestMalformedCredentialSetErrorMessage(t *testing.T) {
	// Missing anchor.
	assert.Equal(t, "malformed credential set: anchor SDK key is missing",
		newMissingAnchorError().Error())

	// Empty credential value.
	assert.Contains(t, NewEmptyCredentialError("sdkKeys", "my-key").Error(), "empty value")

	// The config.SDKKey import is exercised; confirm it still compiles.
	_ = config.SDKKey("sdk-abcd1234")
}
