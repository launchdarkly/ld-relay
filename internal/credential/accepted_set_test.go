package credential

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMalformedCredentialSetErrorMessage(t *testing.T) {
	// Missing anchor.
	assert.Equal(t, "malformed credential set: anchor SDK key is missing",
		newMissingAnchorError().Error())

	// Empty credential value.
	assert.Contains(t, NewEmptyCredentialError("sdkKeys", "my-key").Error(), "empty value")
}
