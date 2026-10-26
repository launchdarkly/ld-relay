package credential

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"

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
	assert.Contains(t, set.sdkKeys, config.SDKKey("sdk"))
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

func TestAcceptedSetBuilderKeepsTheEarlierExpiryForARepeatedValue(t *testing.T) {
	// A payload that lists one credential value twice with different expiries must not let array order
	// decide when the key dies. The shorter life wins, in either order, so a duplicate entry cannot
	// extend a key the same payload says expires sooner.
	soon, later := time.Unix(1000, 0), time.Unix(2000, 0)

	for _, tc := range []struct {
		name           string
		first, second  *time.Time
		expectedExpiry *time.Time
	}{
		{"later then soon", &later, &soon, &soon},
		{"soon then later", &soon, &later, &soon},
		{"permanent then expiring", nil, &soon, &soon},
		{"expiring then permanent", &soon, nil, &soon},
		{"both permanent", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := mustBuild(t, NewAcceptedSetBuilder().
				WithAnchor(SDKKeyParams{Value: "anchor"}).
				WithSDKKey(SDKKeyParams{Value: "dupe", Expiry: tc.first}).
				WithSDKKey(SDKKeyParams{Value: "dupe", Expiry: tc.second}).
				WithMobileKey(MobileKeyParams{Value: "mob-dupe", Expiry: tc.first}).
				WithMobileKey(MobileKeyParams{Value: "mob-dupe", Expiry: tc.second}))

			assert.Equal(t, tc.expectedExpiry, set.sdkKeys[config.SDKKey("dupe")].Expiry)
			assert.Equal(t, tc.expectedExpiry, set.mobileKeys[config.MobileKey("mob-dupe")].Expiry)
		})
	}
}

func TestAcceptedSetBuilderNeverGivesADesignatedKeyAnExpiry(t *testing.T) {
	// The anchor and the primary mobile key are permanent. A later plain add for the same value must
	// not demote either one, whatever expiry it carries, because the rotator will never expire a
	// designated key and a reported expiry it cannot honour misleads an operator.
	expiry := time.Unix(1000, 0)
	set := mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: "sdk", Key: strPtr("anchor-name")}).
		WithSDKKey(SDKKeyParams{Value: "sdk", Key: strPtr("other-name"), Expiry: &expiry}).
		WithPrimaryMobileKey(MobileKeyParams{Value: "mob", Key: strPtr("primary-name")}).
		WithMobileKey(MobileKeyParams{Value: "mob", Key: strPtr("other-name"), Expiry: &expiry}))

	assert.Nil(t, set.sdkKeys[config.SDKKey("sdk")].Expiry)
	assert.Nil(t, set.mobileKeys[config.MobileKey("mob")].Expiry)
	// The designation also keeps the name it was designated with.
	assert.Equal(t, "anchor-name", *set.sdkKeys[config.SDKKey("sdk")].Key)
	assert.Equal(t, "primary-name", *set.mobileKeys[config.MobileKey("mob")].Key)
}

func strPtr(s string) *string { return &s }

// mustBuild builds the set and fails the test if validation rejects it. It is shared by the builder
// tests and the Reconcile tests in rotator_test.go.
func mustBuild(t *testing.T, b *AcceptedSetBuilder) AcceptedSet {
	t.Helper()
	set, err := b.Build()
	require.NoError(t, err)
	return set
}
