package credential

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptedSetBuilderValidation(t *testing.T) {
	// No SDK key at all is malformed: the environment would have nothing to authenticate with.
	var malformed *MalformedCredentialSetError
	_, err := NewAcceptedSetBuilder().
		WithMobileKey(MobileKeyParams{Value: "mob"}).
		WithEnvironmentID(config.EnvironmentID("env")).
		Build()
	require.ErrorAs(t, err, &malformed)

	// An SDK key with no designated anchor is malformed.
	_, err = NewAcceptedSetBuilder().WithSDKKey(SDKKeyParams{Value: "sdk"}).Build()
	require.ErrorAs(t, err, &malformed)

	// WithAnchor adds the key and designates it as the anchor, so Build succeeds.
	set, err := NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: "sdk"}).Build()
	require.NoError(t, err)
	assert.True(t, set.hasSDKKey(config.SDKKey("sdk")))
	assert.Equal(t, config.SDKKey("sdk"), set.anchor)
}

func TestAcceptedSetBuilderDeduplicates(t *testing.T) {
	// Adding the same key more than once (including via WithPrimary*) keeps a single entry, and a
	// WithPrimary* designation overwrites whatever metadata an earlier plain add recorded: a mobile key
	// first listed with an expiry and then designated primary ends up permanent. That is what keeps the
	// designated primary from ever being reported as torn down, mirroring the SDK anchor. It is a builder
	// contract rather than a Reconcile behaviour — BuildAcceptedSet's switch is exclusive, so this
	// interleaving is unreachable from the wire — which is why it is pinned here.
	pastExpiry := time.Unix(1000, 0)
	set := mustBuild(t, NewAcceptedSetBuilder().
		WithSDKKey(SDKKeyParams{Value: "sdk"}).
		WithAnchor(SDKKeyParams{Value: "sdk"}).
		WithSDKKey(SDKKeyParams{Value: "sdk"}).
		WithMobileKey(MobileKeyParams{Value: "mob", Expiry: &pastExpiry}).
		WithPrimaryMobileKey(MobileKeyParams{Value: "mob"}))

	assert.Len(t, set.sdkKeys, 1)
	assert.Len(t, set.mobileKeys, 1)
	assert.Equal(t, config.SDKKey("sdk"), set.anchor)
	assert.Equal(t, config.MobileKey("mob"), set.primaryMobileKey)
	assert.Nil(t, set.mobileKeys[config.MobileKey("mob")].Expiry,
		"the designated primary mobile key is always permanent, overwriting a prior entry's expiry")
}

// mustBuild builds the set and fails the test if validation rejects it. It is shared by the builder
// tests and the Reconcile tests in rotator_test.go.
func mustBuild(t *testing.T, b *AcceptedSetBuilder) AcceptedSet {
	t.Helper()
	set, err := b.Build()
	require.NoError(t, err)
	return set
}
