package envfactory

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expiry1 is a fixed future timestamp used in tests to represent an expiring key.
var expiry1 = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

// mustBuild builds the AcceptedSet from b, failing the test if Build returns an error. It is used to
// construct the expected set for comparison.
func mustBuild(t *testing.T, b *credential.AcceptedSetBuilder) credential.AcceptedSet {
	t.Helper()
	set, err := b.Build()
	require.NoError(t, err)
	return set
}

// makeParams is a convenience builder for EnvironmentParams test fixtures.
// sdkKey is the anchor, sdkKeys are the full accepted set (must include the anchor),
// mobileKey is the single (primary) mobile key to include.
func makeParams(sdkKey config.SDKKey, sdkKeys []AcceptedSDKKey, mobileKey config.MobileKey) EnvironmentParams {
	mob := []AcceptedMobileKey{}
	if mobileKey.Defined() {
		mob = []AcceptedMobileKey{{Value: mobileKey}}
	}
	return EnvironmentParams{
		EnvID:              config.EnvironmentID("env-abc"),
		SDKKey:             sdkKey,
		MobileKey:          mobileKey,
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

	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"})) // mobile has no identifier in makeParams fixture
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_MultipleKeys verifies that multiple accepted SDK keys (anchor + non-anchor
// permanent + expiring non-anchor) are all included in the returned AcceptedSet.
func TestBuildAcceptedSet_MultipleKeys(t *testing.T) {
	params := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "default", Value: "sdk-anchor"},
			{Key: "service-a", Value: "sdk-service-a"},          // permanent, non-anchor
			{Key: "old-key", Value: "sdk-old", Expiry: expiry1}, // expiring, non-anchor
		},
		"mob-primary",
	)
	set, anchor, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	assert.Equal(t, config.SDKKey("sdk-anchor"), anchor)

	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-service-a", Key: util.PtrOrNil("service-a")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-old", Key: util.PtrOrNil("old-key"), Expiry: util.PtrOrNil(expiry1)}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_Rename verifies that a rename — same credential value, different identifier
// — updates only the identifier in the AcceptedSet, not the accepted credential itself. The sets
// produced before and after a rename carry the same credentials but different identifier maps.
func TestBuildAcceptedSet_Rename(t *testing.T) {
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
	// The credential content is the same — only the identifier differs.
	// When Reconcile applies the new set the display name is refreshed but no credential is added or removed.
	assert.NotEqual(t, setOld, setNew, "rename changes the identifier map, so the AcceptedSets differ")
	expectedOld := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("old-name")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expectedOld, setOld)
	expectedNew := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("new-name")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expectedNew, setNew)
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
	expectedPermanent := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-old", Key: util.PtrOrNil("old-key")}). // permanent, no expiry
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expectedPermanent, setNoExpiry)

	// Sanity: the expiring and non-expiring versions are different.
	assert.NotEqual(t, setWithExpiry, setNoExpiry)
}

// TestBuildAcceptedSet_AnchorNotInArray verifies that a defined anchor absent from the sdkKeys[] array
// yields a *credential.MalformedCredentialSetError: the payload is structurally inconsistent (the
// designated anchor is not in the authoritative array), so it must be rejected rather than silently
// synthesized into the set.
func TestBuildAcceptedSet_AnchorNotInArray(t *testing.T) {
	params := makeParams(
		"sdk-anchor",
		[]AcceptedSDKKey{
			{Key: "other-key", Value: "sdk-other"}, // anchor NOT in the array
		},
		"mob-primary",
	)
	_, _, err := BuildAcceptedSet(params)

	require.Error(t, err)
	var malformed *credential.MalformedCredentialSetError
	require.True(t, errors.As(err, &malformed))
	assert.Contains(t, malformed.Error(), "not present in sdkKeys[]")
}

// TestBuildAcceptedSet_PrimaryMobileNotInArray verifies the mobile analogue of the anchor invariant:
// a defined mobKey absent from mobileKeys[] is rejected. Without this guard the primary mobile key
// would be silently left undesignated, clearing it on reconcile and breaking event forwarding.
func TestBuildAcceptedSet_PrimaryMobileNotInArray(t *testing.T) {
	params := EnvironmentParams{
		EnvID:           "env-abc",
		SDKKey:          "sdk-anchor",
		MobileKey:       "mob-primary", // defined...
		AcceptedSDKKeys: []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
		AcceptedMobileKeys: []AcceptedMobileKey{
			{Key: "other", Value: "mob-other"}, // ...but NOT in the array
		},
	}
	_, _, err := BuildAcceptedSet(params)

	require.Error(t, err)
	var malformed *credential.MalformedCredentialSetError
	require.True(t, errors.As(err, &malformed))
	assert.Contains(t, malformed.Error(), "not present in mobileKeys[]")
}

// TestBuildAcceptedSet_NoMobileKey verifies that an environment with no mobile key (e.g. a
// server-side-only environment) is valid: ToParams must not synthesize a phantom empty mobileKeys
// entry that BuildAcceptedSet would reject as malformed.
func TestBuildAcceptedSet_NoMobileKey(t *testing.T) {
	rep := EnvironmentRep{
		EnvID:  "env-abc",
		SDKKey: SDKKeyRep{Value: config.SDKKey("sdk-anchor")},
		// no MobKey, no MobileKeys
	}
	set, anchor, err := BuildAcceptedSet(rep.ToParams())

	require.NoError(t, err)
	assert.Equal(t, config.SDKKey("sdk-anchor"), anchor)

	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor"}))
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_AnchorUndefined verifies that an undefined anchor (empty SDKKey) yields a
// *credential.MalformedCredentialSetError: no anchor was designated, so Build rejects the set.
func TestBuildAcceptedSet_AnchorUndefined(t *testing.T) {
	params := makeParams(
		"", // undefined anchor
		[]AcceptedSDKKey{
			{Key: "key-a", Value: "sdk-a"},
		},
		"mob-primary",
	)
	_, _, err := BuildAcceptedSet(params)

	require.Error(t, err)
	var malformed *credential.MalformedCredentialSetError
	require.True(t, errors.As(err, &malformed))
	assert.Contains(t, malformed.Error(), "anchor SDK key is missing")
}

// TestBuildAcceptedSet_NoSDKKeys verifies that when neither an anchor nor any array SDK keys are
// present, Build returns an error (the set has no SDK key at all).
func TestBuildAcceptedSet_NoSDKKeys(t *testing.T) {
	params := EnvironmentParams{
		SDKKey:             "", // undefined anchor
		AcceptedSDKKeys:    []AcceptedSDKKey{},
		AcceptedMobileKeys: []AcceptedMobileKey{},
	}
	_, _, err := BuildAcceptedSet(params)
	require.Error(t, err, "a set with no SDK key at all must be rejected")
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

	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-new-anchor", Key: util.PtrOrNil("new-default")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-b", Key: util.PtrOrNil("service-b")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-c", Key: util.PtrOrNil("service-c")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_AnchorNeverExpiring verifies the invariant defense: even if a payload
// carries an expiry on the anchor's own entry, the anchor is added as a permanent key, never
// as an expiring one.
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

	// Anchor is permanent (WithAnchor), not expiring — identical to a payload with no anchor expiry.
	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-service-a", Key: util.PtrOrNil("service-a")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_MultipleMobileKeys verifies that all accepted mobile keys are included,
// exercising the len(AcceptedMobileKeys) > 1 path, and that the wire's mobKey is designated as the
// primary mobile key.
func TestBuildAcceptedSet_MultipleMobileKeys(t *testing.T) {
	params := EnvironmentParams{
		EnvID:           "env-abc",
		SDKKey:          "sdk-anchor",
		MobileKey:       "mob-primary",
		AcceptedSDKKeys: []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
		AcceptedMobileKeys: []AcceptedMobileKey{
			{Key: "mob-1", Value: "mob-primary"},
			{Key: "mob-2", Value: "mob-secondary"},
		},
	}
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithMobileKey(credential.MobileKeyParams{Value: "mob-secondary", Key: util.PtrOrNil("mob-2")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary", Key: util.PtrOrNil("mob-1")}))
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_ExpiringMobileKey verifies that a mobile key carrying a non-zero Expiry is
// plumbed through as an expiring key (parallel to the expiring-SDK-key path), while the permanent
// primary mobile key is designated. This is what makes per-key mobile expiry work end-to-end:
// params carry it → BuildAcceptedSet plumbs it into the AcceptedSet → Reconcile acts on it.
func TestBuildAcceptedSet_ExpiringMobileKey(t *testing.T) {
	params := EnvironmentParams{
		EnvID:           "env-abc",
		SDKKey:          "sdk-anchor",
		MobileKey:       "mob-primary",
		AcceptedSDKKeys: []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
		AcceptedMobileKeys: []AcceptedMobileKey{
			{Key: "mob-1", Value: "mob-primary"},                // permanent primary
			{Key: "mob-old", Value: "mob-old", Expiry: expiry1}, // expiring
		},
	}
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithMobileKey(credential.MobileKeyParams{Value: "mob-old", Key: util.PtrOrNil("mob-old"), Expiry: util.PtrOrNil(expiry1)}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary", Key: util.PtrOrNil("mob-1")}))
	assert.Equal(t, expected, set, "expiring mobile key must land as an expiring key in the set")
}
