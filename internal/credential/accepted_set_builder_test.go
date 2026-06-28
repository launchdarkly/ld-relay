package credential

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptedSetBuilderValidation(t *testing.T) {
	// No SDK key at all is a caller error.
	_, err := NewAcceptedSetBuilder().
		WithMobileKey(MobileKeyParams{Value: "mob"}).
		WithEnvironmentID(config.EnvironmentID("env")).
		Build()
	require.ErrorIs(t, err, errAcceptedSetMissingSDKKey)

	// An SDK key with no designated anchor is malformed.
	var malformed *MalformedCredentialSetError
	_, err = NewAcceptedSetBuilder().WithSDKKey(SDKKeyParams{Value: "sdk"}).Build()
	require.ErrorAs(t, err, &malformed)

	// WithPrimarySDKKey adds the key and designates it as the anchor, so Build succeeds.
	set, err := NewAcceptedSetBuilder().WithPrimarySDKKey(SDKKeyParams{Value: "sdk"}).Build()
	require.NoError(t, err)
	assert.True(t, set.hasSDKKey(config.SDKKey("sdk")))
	assert.Equal(t, config.SDKKey("sdk"), set.primarySdkKey)
}

func TestAcceptedSetBuilderDeduplicates(t *testing.T) {
	// Adding the same key more than once (including via WithPrimary*) keeps a single entry.
	set := mustBuild(t, NewAcceptedSetBuilder().
		WithSDKKey(SDKKeyParams{Value: "sdk"}).
		WithPrimarySDKKey(SDKKeyParams{Value: "sdk"}).
		WithSDKKey(SDKKeyParams{Value: "sdk"}).
		WithMobileKey(MobileKeyParams{Value: "mob"}).
		WithPrimaryMobileKey(MobileKeyParams{Value: "mob"}))

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

// sdkP / sdkPExp / mobP / mobPExp build credential param structs for the Reconcile tests, mapping an
// empty identifier to a nil Key pointer.
func sdkP(value config.SDKKey, key string) SDKKeyParams {
	return SDKKeyParams{Value: value, Key: strPtrOrNil(key)}
}
func sdkPExp(value config.SDKKey, key string, expiry time.Time) SDKKeyParams {
	return SDKKeyParams{Value: value, Key: strPtrOrNil(key), Expiry: &expiry}
}
func mobP(value config.MobileKey, key string) MobileKeyParams {
	return MobileKeyParams{Value: value, Key: strPtrOrNil(key)}
}
func mobPExp(value config.MobileKey, key string, expiry time.Time) MobileKeyParams {
	return MobileKeyParams{Value: value, Key: strPtrOrNil(key), Expiry: &expiry}
}
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
