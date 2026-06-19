package envfactory

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expiry1 is a fixed future timestamp used in tests to represent an expiring key.
var expiry1 = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

// makeParams is a convenience builder for EnvironmentParams test fixtures.
// sdkKey is the anchor, sdkKeys are the full accepted set (must include the anchor),
// mobileKey is the single mobile key to include.
func makeParams(sdkKey config.SDKKey, sdkKeys []AcceptedSDKKey, mobileKey config.MobileKey) EnvironmentParams {
	mob := []AcceptedMobileKey{}
	if mobileKey.Defined() {
		mob = []AcceptedMobileKey{{Value: mobileKey}}
	}
	return EnvironmentParams{
		EnvID:              config.EnvironmentID("env-abc"),
		SDKKey:             sdkKey,
		AcceptedSDKKeys:    sdkKeys,
		AcceptedMobileKeys: mob,
	}
}

// TestBuildAcceptedSet_HappyPath verifies the basic case: a single permanent SDK key that is the
// anchor, plus a mobile key and env ID.
func TestBuildAcceptedSet_HappyPath(t *testing.T) {
	params := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
		"mob-primary",
	)
	set, anchor, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	assert.Equal(t, config.SDKKey("sdk-anchor"), anchor)

	expected := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-anchor").
		WithMobileKey("mob-primary")
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_MultipleKeys verifies that multiple accepted SDK keys (anchor + non-anchor
// permanent + expiring non-anchor) are all included in the returned AcceptedSet.
func TestBuildAcceptedSet_MultipleKeys(t *testing.T) {
	params := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "default", Value: "sdk-anchor"},
			{Key: "service-a", Value: "sdk-service-a"},                    // permanent, non-anchor
			{Key: "old-key", Value: "sdk-old", Expiry: expiry1},          // expiring, non-anchor
		},
		"mob-primary",
	)
	set, anchor, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	assert.Equal(t, config.SDKKey("sdk-anchor"), anchor)

	expected := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-anchor").
		WithSDKKey("sdk-service-a").
		WithExpiringSDKKey("sdk-old", expiry1).
		WithMobileKey("mob-primary")
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_Rename verifies that a rename — same credential value, different key
// identifier — is a no-op: the returned AcceptedSet is identical regardless of the identifier.
func TestBuildAcceptedSet_Rename(t *testing.T) {
	// Build AcceptedSet for the "before" and "after" of a rename.
	paramsOldName := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{{Key: "old-name", Value: "sdk-anchor"}},
		"mob-primary",
	)
	paramsNewName := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{{Key: "new-name", Value: "sdk-anchor"}},
		"mob-primary",
	)

	setOld, _, errOld := BuildAcceptedSet(paramsOldName)
	setNew, _, errNew := BuildAcceptedSet(paramsNewName)

	require.NoError(t, errOld)
	require.NoError(t, errNew)
	assert.Equal(t, setOld, setNew, "rename (same value, different key identifier) should produce the same AcceptedSet")
}

// TestBuildAcceptedSet_Deexpiry verifies that removing the expiry from an existing key (a
// previously expiring key that is now permanent) results in the key being permanent in the
// returned AcceptedSet. The "cancel scheduled drop" effect is realized when ReconcileCredentials
// applies this set to the Rotator.
func TestBuildAcceptedSet_Deexpiry(t *testing.T) {
	// "Before" state: sdk-old has an expiry.
	paramsWithExpiry := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "default", Value: "sdk-anchor"},
			{Key: "old-key", Value: "sdk-old", Expiry: expiry1},
		},
		"mob-primary",
	)
	// "After" state: sdk-old's expiry is removed — it is now permanent.
	paramsNoExpiry := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "default", Value: "sdk-anchor"},
			{Key: "old-key", Value: "sdk-old"}, // Expiry zero = permanent
		},
		"mob-primary",
	)

	setWithExpiry, _, errWithExpiry := BuildAcceptedSet(paramsWithExpiry)
	setNoExpiry, _, errNoExpiry := BuildAcceptedSet(paramsNoExpiry)

	require.NoError(t, errWithExpiry)
	require.NoError(t, errNoExpiry)

	// The set built without expiry must include sdk-old as a permanent key.
	expectedPermanent := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-anchor").
		WithSDKKey("sdk-old"). // permanent, no expiry
		WithMobileKey("mob-primary")
	assert.Equal(t, expectedPermanent, setNoExpiry)

	// Sanity: the expiring and non-expiring versions are different.
	assert.NotEqual(t, setWithExpiry, setNoExpiry)
}

// TestBuildAcceptedSet_MalformedPayload verifies that when the anchor (params.SDKKey) is not
// present in AcceptedSDKKeys, BuildAcceptedSet returns a *relayenv.MalformedCredentialSetError
// and an empty AcceptedSet.
func TestBuildAcceptedSet_MalformedPayload(t *testing.T) {
	params := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "other-key", Value: "sdk-other"}, // anchor NOT here
		},
		"mob-primary",
	)
	set, anchor, err := BuildAcceptedSet(params)

	require.Error(t, err)
	var malformed *relayenv.MalformedCredentialSetError
	require.True(t, errors.As(err, &malformed), "error should be *relayenv.MalformedCredentialSetError")
	assert.Equal(t, config.SDKKey("sdk-anchor"), anchor)
	assert.Equal(t, relayenv.NewAcceptedSet(), set, "AcceptedSet must be empty on malformed payload")
	// The error must carry the offending anchor so the caller/log identifies it (masked).
	assert.Equal(t, config.SDKKey("sdk-anchor"), malformed.Anchor)
	assert.Contains(t, malformed.Error(), "not present in the accepted set")
}

// TestBuildAcceptedSet_MalformedPayload_AnchorUndefined verifies that an undefined anchor
// (empty SDKKey) is treated as a malformed payload.
func TestBuildAcceptedSet_MalformedPayload_AnchorUndefined(t *testing.T) {
	params := makeParams(
		"", // undefined anchor
		[]AcceptedSDKKey{
			{Key: "key-a", Value: "sdk-a"},
		},
		"mob-primary",
	)
	_, _, err := BuildAcceptedSet(params)

	require.Error(t, err)
	var malformed *relayenv.MalformedCredentialSetError
	require.True(t, errors.As(err, &malformed))
	// An undefined anchor must produce the "missing" message, not the "not present" one. This
	// only holds if Anchor is an untyped nil — a boxed zero-value config.SDKKey would be non-nil
	// and route Error() down the wrong branch.
	assert.Nil(t, malformed.Anchor)
	assert.Contains(t, malformed.Error(), "anchor SDK key is missing")
}

// TestBuildAcceptedSet_MixedUpdate verifies add + re-anchor + remove in a single params update
// produces an AcceptedSet that contains the right keys in the right state. The ordering
// (add → re-anchor → remove) is enforced by ReconcileCredentials when it consumes this set;
// this test only asserts the AcceptedSet content.
func TestBuildAcceptedSet_MixedUpdate(t *testing.T) {
	// New state after the patch:
	//   - sdk-new-anchor is the new anchor (re-anchor)
	//   - sdk-b carries over unchanged
	//   - sdk-c is newly added
	//   - sdk-old-anchor is gone (remove)
	params := makeParams(
		"sdk-new-anchor",
		[]AcceptedSDKKey{
			{Key: "new-default", Value: "sdk-new-anchor"}, // re-anchor
			{Key: "service-b", Value: "sdk-b"},            // unchanged
			{Key: "service-c", Value: "sdk-c"},            // added
		},
		"mob-primary",
	)
	set, anchor, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	assert.Equal(t, config.SDKKey("sdk-new-anchor"), anchor)

	expected := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-new-anchor").
		WithSDKKey("sdk-b").
		WithSDKKey("sdk-c").
		WithMobileKey("mob-primary")
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_AnchorNeverExpiring verifies the §4.2 invariant defense: even if a
// payload carries an expiry on the anchor's own entry, the anchor is added as a permanent key,
// never as an expiring one.
func TestBuildAcceptedSet_AnchorNeverExpiring(t *testing.T) {
	params := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "default", Value: "sdk-anchor", Expiry: expiry1}, // anchor with a bogus expiry
			{Key: "service-a", Value: "sdk-service-a"},
		},
		"mob-primary",
	)
	set, anchor, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	assert.Equal(t, config.SDKKey("sdk-anchor"), anchor)

	// Anchor is permanent (WithSDKKey), not expiring — identical to a payload with no anchor expiry.
	expected := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-anchor").
		WithSDKKey("sdk-service-a").
		WithMobileKey("mob-primary")
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_MultipleMobileKeys verifies that all accepted mobile keys are included,
// exercising the len(AcceptedMobileKeys) > 1 path.
func TestBuildAcceptedSet_MultipleMobileKeys(t *testing.T) {
	params := EnvironmentParams{
		EnvID:           "env-abc",
		SDKKey:          "sdk-anchor",
		AcceptedSDKKeys: []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
		AcceptedMobileKeys: []AcceptedMobileKey{
			{Key: "mob-1", Value: "mob-primary"},
			{Key: "mob-2", Value: "mob-secondary"},
		},
	}
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	expected := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-anchor").
		WithMobileKey("mob-primary").
		WithMobileKey("mob-secondary")
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_TrustTheArray verifies that the legacy sdkKey.expiring slot is not
// consulted: when EnvironmentParams.ExpiringSDKKey is populated (from the legacy field) but
// AcceptedSDKKeys does NOT contain that key, the key is absent from the returned AcceptedSet.
func TestBuildAcceptedSet_TrustTheArray(t *testing.T) {
	// Simulate an old-relay payload where ExpiringSDKKey is populated from sdkKey.expiring,
	// but AcceptedSDKKeys only has the anchor (no expiring key in the array).
	params := EnvironmentParams{
		EnvID:  "env-abc",
		SDKKey: "sdk-anchor",
		ExpiringSDKKey: ExpiringSDKKey{ // legacy field — must NOT be consulted
			Key:        "sdk-legacy-expiring",
			Expiration: expiry1,
		},
		AcceptedSDKKeys:    []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
		AcceptedMobileKeys: []AcceptedMobileKey{{Value: "mob-primary"}},
	}

	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	expected := relayenv.NewAcceptedSet().
		WithEnvironmentID("env-abc").
		WithSDKKey("sdk-anchor").
		WithMobileKey("mob-primary")
	assert.Equal(t, expected, set, "legacy sdkKey.expiring slot must not appear in AcceptedSet")
}

// TestBuildAcceptedSet_EmptyArrays verifies the edge case where AcceptedSDKKeys is empty:
// the anchor cannot be found, so a malformed-payload error is returned.
func TestBuildAcceptedSet_EmptyArrays(t *testing.T) {
	// Edge case: AcceptedSDKKeys is empty (which means malformed, since anchor can't be found).
	params := EnvironmentParams{
		SDKKey:             "sdk-anchor",
		AcceptedSDKKeys:    []AcceptedSDKKey{},
		AcceptedMobileKeys: []AcceptedMobileKey{},
	}
	_, _, err := BuildAcceptedSet(params)
	require.Error(t, err, "empty AcceptedSDKKeys should be malformed since anchor is not present")
}
