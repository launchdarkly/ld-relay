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
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
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
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
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

// TestBuildAcceptedSet_MalformedPayloads enumerates every structurally malformed payload shape
// BuildAcceptedSet rejects. Each row guards a different silent failure, so each asserts the
// distinguishing part of its message as well as the error type: relay must reject the payload loudly
// rather than synthesize a credential-short or structurally inconsistent environment from it.
func TestBuildAcceptedSet_MalformedPayloads(t *testing.T) {
	tests := []struct {
		name             string
		params           EnvironmentParams
		wantMsgSubstring string
	}{
		{
			// A defined anchor absent from the authoritative sdkKeys[] array is structurally
			// inconsistent, so it must be rejected rather than silently synthesized into the set.
			name: "anchor not present in sdkKeys[]",
			params: makeParams(
				"sdk-anchor",
				[]AcceptedSDKKey{{Key: "other-key", Value: "sdk-other"}}, // anchor NOT in the array
				"mob-primary",
			),
			wantMsgSubstring: "not present in sdkKeys[]",
		},
		{
			// The mobile analogue of the anchor invariant. Without this guard the primary mobile key
			// would be silently left undesignated, clearing it on reconcile and breaking event forwarding.
			name: "primary mobile key not present in mobileKeys[]",
			params: EnvironmentParams{
				EnvID:           "env-abc",
				SDKKey:          "sdk-anchor",
				MobileKey:       "mob-primary", // defined...
				AcceptedSDKKeys: []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
				AcceptedMobileKeys: []AcceptedMobileKey{
					{Key: "other", Value: "mob-other"}, // ...but NOT in the array
				},
			},
			wantMsgSubstring: "not present in mobileKeys[]",
		},
		{
			// The complement of the guard above: accepting a non-empty mobileKeys[] with no designated
			// primary would clear the rotator's primary mobile key with no repoint, so event forwarding
			// would keep using the previous (possibly revoked) primary instead of rejecting loudly.
			name: "mobileKeys[] non-empty with no designated primary",
			params: EnvironmentParams{
				EnvID:           "env-abc",
				SDKKey:          "sdk-anchor",
				MobileKey:       "", // no primary designated...
				AcceptedSDKKeys: []AcceptedSDKKey{{Key: "default", Value: "sdk-anchor"}},
				AcceptedMobileKeys: []AcceptedMobileKey{
					{Key: "mob-1", Value: "mob-primary"}, // ...but the array is non-empty
				},
			},
			wantMsgSubstring: "no primary mobile key is designated",
		},
		{
			// An undefined anchor never matches a (defined) array value, so none is ever designated and
			// Build rejects the set.
			name: "anchor undefined",
			params: makeParams(
				"", // undefined anchor
				[]AcceptedSDKKey{{Key: "key-a", Value: "sdk-a"}},
				"mob-primary",
			),
			wantMsgSubstring: "anchor SDK key is missing",
		},
		{
			// No SDK key survives at all, so the environment would have nothing to authenticate with.
			name: "no usable SDK key: empty array",
			params: EnvironmentParams{
				SDKKey:             "", // undefined anchor
				AcceptedSDKKeys:    []AcceptedSDKKey{},
				AcceptedMobileKeys: []AcceptedMobileKey{},
			},
			wantMsgSubstring: "no usable SDK key in sdkKeys[]",
		},
		{
			// The same end state reached by filtering rather than by an empty payload. It is worth
			// pinning separately: a filtered-to-empty array is a payload problem, not a caller mistake.
			name: "no usable SDK key: every entry view-scoped",
			params: EnvironmentParams{
				SDKKey:             "", // undefined anchor
				AcceptedSDKKeys:    []AcceptedSDKKey{{Key: "view-sdk", Value: "sdk-viewy", HasViews: true}},
				AcceptedMobileKeys: []AcceptedMobileKey{},
			},
			wantMsgSubstring: "no usable SDK key in sdkKeys[]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := BuildAcceptedSet(tt.params)

			require.Error(t, err, "a structurally malformed payload must be rejected")
			var malformed *credential.MalformedCredentialSetError
			require.True(t, errors.As(err, &malformed),
				"every rejection from BuildAcceptedSet is a malformed-payload error")
			assert.Contains(t, malformed.Error(), tt.wantMsgSubstring)
		})
	}
}

// TestBuildAcceptedSet_NoMobileKey verifies that an environment with no mobile key (e.g. a
// server-side-only environment) is valid: ToParams must not synthesize a phantom empty mobileKeys
// entry that BuildAcceptedSet would reject as malformed. It also pins the boundary of the
// non-empty-mobileKeys[]-without-a-primary guard above — that guard fires only when the array is
// non-empty, and ToParams synthesizes exactly the empty array this case relies on.
func TestBuildAcceptedSet_NoMobileKey(t *testing.T) {
	rep := EnvironmentRep{
		EnvID:  "env-abc",
		SDKKey: SDKKeyRep{Value: config.SDKKey("sdk-anchor")},
		// no MobKey, no MobileKeys
	}
	set, _, err := BuildAcceptedSet(rep.ToParams())

	require.NoError(t, err)
	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor"}))
	assert.Equal(t, expected, set)
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
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
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
	set, _, err := BuildAcceptedSet(params)

	require.NoError(t, err)
	// Anchor is permanent (WithAnchor), not expiring — identical to a payload with no anchor expiry.
	expected := mustBuild(t, credential.NewAcceptedSetBuilder().
		WithEnvironmentID("env-abc").
		WithAnchor(credential.SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
		WithSDKKey(credential.SDKKeyParams{Value: "sdk-service-a", Key: util.PtrOrNil("service-a")}).
		WithPrimaryMobileKey(credential.MobileKeyParams{Value: "mob-primary"}))
	assert.Equal(t, expected, set)
}

// TestBuildAcceptedSet_ExpiringMobileKey verifies that a mobile key carrying a non-zero Expiry is
// plumbed through as an expiring key (parallel to the expiring-SDK-key path), while the permanent
// primary mobile key is designated. This is what makes per-key mobile expiry work end-to-end:
// params carry it → BuildAcceptedSet plumbs it into the AcceptedSet → Reconcile acts on it. It also
// exercises the len(AcceptedMobileKeys) > 1 path and the designation of the wire's mobKey as primary.
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

// TestBuildAcceptedSet_ViewScopedKeys covers the ingestion filter across both arrays. A key scoped to a
// view may only see a subset of the environment's flags; relay serves the whole environment payload, so
// admitting one would silently over-deliver. Such a key is therefore never added to the set, and an SDK
// presenting it is rejected because the credential is simply absent from the lookup map. The dropped
// keys are returned to the caller so it can WARN.
func TestBuildAcceptedSet_ViewScopedKeys(t *testing.T) {
	const (
		anchor  = config.SDKKey("sdk-anchor")
		primary = config.MobileKey("mob-primary")
	)

	// The four entries every case starts from; individual cases add view-scoped entries alongside them.
	anchorEntry := AcceptedSDKKey{Key: "default-sdk", Value: anchor}
	extraSDK := AcceptedSDKKey{Key: "service-a", Value: "sdk-service-a"}
	primaryEntry := AcceptedMobileKey{Key: "default-mob", Value: primary}
	extraMob := AcceptedMobileKey{Key: "mob-extra", Value: "mob-extra"}

	// base is the set with only the two designated keys; cases add whatever survived the filter.
	base := func() *credential.AcceptedSetBuilder {
		return credential.NewAcceptedSetBuilder().
			WithEnvironmentID("env-abc").
			WithAnchor(credential.SDKKeyParams{Value: anchor, Key: util.PtrOrNil("default-sdk")}).
			WithPrimaryMobileKey(credential.MobileKeyParams{Value: primary, Key: util.PtrOrNil("default-mob")})
	}
	acceptedExtraSDK := credential.SDKKeyParams{Value: extraSDK.Value, Key: util.PtrOrNil(extraSDK.Key)}
	acceptedExtraMob := credential.MobileKeyParams{Value: extraMob.Value, Key: util.PtrOrNil(extraMob.Key)}

	// baseWithExtras is base plus both non-designated keys — the expectation for every case where the
	// filter drops nothing that was going to be accepted anyway.
	baseWithExtras := func() *credential.AcceptedSetBuilder {
		return base().WithSDKKey(acceptedExtraSDK).WithMobileKey(acceptedExtraMob)
	}

	tests := []struct {
		name         string
		sdkKeys      []AcceptedSDKKey
		mobileKeys   []AcceptedMobileKey
		wantSet      func() *credential.AcceptedSetBuilder
		wantRejected []string
	}{
		{
			name:         "view-scoped non-anchor SDK key is excluded",
			sdkKeys:      []AcceptedSDKKey{anchorEntry, extraSDK, {Key: "view-sdk", Value: "sdk-viewy", HasViews: true}},
			mobileKeys:   []AcceptedMobileKey{primaryEntry, extraMob},
			wantSet:      baseWithExtras,
			wantRejected: []string{"view-sdk"},
		},
		{
			name:         "view-scoped non-primary mobile key is excluded",
			sdkKeys:      []AcceptedSDKKey{anchorEntry, extraSDK},
			mobileKeys:   []AcceptedMobileKey{primaryEntry, extraMob, {Key: "view-mob", Value: "mob-viewy", HasViews: true}},
			wantSet:      baseWithExtras,
			wantRejected: []string{"view-mob"},
		},
		{
			// Both arrays filter in one pass; SDK keys are walked first, hence the order.
			name:       "view-scoped keys in both arrays are excluded",
			sdkKeys:    []AcceptedSDKKey{anchorEntry, {Key: "view-sdk", Value: "sdk-viewy", HasViews: true}},
			mobileKeys: []AcceptedMobileKey{primaryEntry, {Key: "view-mob", Value: "mob-viewy", HasViews: true}},
			wantSet:    base,
			// Every non-designated key is view-scoped, so only the anchor and primary survive.
			wantRejected: []string{"view-sdk", "view-mob"},
		},
		{
			// A view-scoped key is dropped outright rather than being admitted as an expiring key —
			// the marker takes precedence over expiry handling.
			name:         "view-scoped key carrying an expiry is still excluded",
			sdkKeys:      []AcceptedSDKKey{anchorEntry, {Key: "view-sdk", Value: "sdk-viewy", Expiry: expiry1, HasViews: true}},
			mobileKeys:   []AcceptedMobileKey{primaryEntry},
			wantSet:      base,
			wantRejected: []string{"view-sdk"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := EnvironmentParams{
				EnvID:              "env-abc",
				SDKKey:             anchor,
				MobileKey:          primary,
				AcceptedSDKKeys:    tt.sdkKeys,
				AcceptedMobileKeys: tt.mobileKeys,
			}

			set, rejected, err := BuildAcceptedSet(params)

			// A view-scoped key is filtered, never fatal — the environment always keeps operating.
			require.NoError(t, err)
			assert.Equal(t, mustBuild(t, tt.wantSet()), set)
			assert.Equal(t, tt.wantRejected, rejected)
		})
	}
}

// TestBuildAcceptedSet_ViewScopedKeysEmptyOnError verifies that the rejected list is empty whenever an
// error is returned. The caller discards the whole payload and preserves its previous credentials in
// that case, so reporting keys it did not act on would produce a misleading WARN.
func TestBuildAcceptedSet_ViewScopedKeysEmptyOnError(t *testing.T) {
	// The anchor is absent from sdkKeys[] — malformed — and a view-scoped entry is present alongside.
	params := EnvironmentParams{
		EnvID:              "env-abc",
		SDKKey:             "sdk-anchor",
		AcceptedSDKKeys:    []AcceptedSDKKey{{Key: "view-sdk", Value: "sdk-viewy", HasViews: true}},
		AcceptedMobileKeys: []AcceptedMobileKey{},
	}

	_, rejected, err := BuildAcceptedSet(params)

	require.Error(t, err)
	assert.Empty(t, rejected)
}
