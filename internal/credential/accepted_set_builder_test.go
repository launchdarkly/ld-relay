package credential

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptedSetBuilderValidation(t *testing.T) {
	// No SDK key at all is a caller error.
	_, err := NewAcceptedSetBuilder().
		WithMobileKey(config.MobileKey("mob")).
		WithEnvironmentID(config.EnvironmentID("env")).
		Build()
	require.ErrorIs(t, err, errAcceptedSetMissingSDKKey)

	// An SDK key with no designated anchor is malformed.
	var malformed *MalformedCredentialSetError
	_, err = NewAcceptedSetBuilder().WithSDKKey(config.SDKKey("sdk")).Build()
	require.ErrorAs(t, err, &malformed)

	// WithPrimarySDKKey adds the key and designates it as the anchor, so Build succeeds.
	set, err := NewAcceptedSetBuilder().WithPrimarySDKKey(config.SDKKey("sdk")).Build()
	require.NoError(t, err)
	assert.True(t, set.hasSDKKey(config.SDKKey("sdk")))
	assert.Equal(t, config.SDKKey("sdk"), set.primarySdkKey)
}

func TestAcceptedSetBuilderDeduplicates(t *testing.T) {
	// Adding the same key more than once (including via WithPrimary*) keeps a single entry.
	set := mustBuild(t, NewAcceptedSetBuilder().
		WithSDKKey(config.SDKKey("sdk")).
		WithPrimarySDKKey(config.SDKKey("sdk")).
		WithSDKKey(config.SDKKey("sdk")).
		WithMobileKey(config.MobileKey("mob")).
		WithPrimaryMobileKey(config.MobileKey("mob")))

	assert.Len(t, set.sdkKeys, 1)
	assert.Len(t, set.mobileKeys, 1)
	assert.Equal(t, config.SDKKey("sdk"), set.primarySdkKey)
	assert.Equal(t, config.MobileKey("mob"), set.primaryMobileKey)
}

// mustBuild builds the set and fails the test if validation rejects it. It is shared by the builder
// tests and the Reconcile tests in rotator_test.go.
func mustBuild(t *testing.T, b *AcceptedSetBuilder) AcceptedSet {
	t.Helper()
	set, err := b.Build()
	require.NoError(t, err)
	return set
}
