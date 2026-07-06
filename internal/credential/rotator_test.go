package credential

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRotator() *Rotator {
	return NewRotator(ldlogtest.NewMockLog().Loggers)
}

func TestNewRotator(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	assert.NotNil(t, rotator)
}

func TestInitializePopulatesAcceptedSets(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	sdkKey := config.SDKKey("sdk-test-key")
	mobileKey := config.MobileKey("mob-test-key")
	envID := config.EnvironmentID("env-test-id")

	rotator.Initialize([]SDKCredential{sdkKey, mobileKey, envID})

	// Verify accepted SDK key set: one entry, no expiry.
	assert.Len(t, rotator.acceptedSDKKeys, 1)
	if info, ok := rotator.acceptedSDKKeys[sdkKey]; assert.True(t, ok, "acceptedSDKKeys should contain the initialized SDK key") {
		assert.Nil(t, info.Expiry, "a key initialized without expiry should have nil expiry in AcceptedKey")
	}

	// Verify accepted mobile key set: one entry, no expiry.
	assert.Len(t, rotator.acceptedMobileKeys, 1)
	if info, ok := rotator.acceptedMobileKeys[mobileKey]; assert.True(t, ok, "acceptedMobileKeys should contain the initialized mobile key") {
		assert.Nil(t, info.Expiry, "a key initialized without expiry should have nil expiry in AcceptedKey")
	}

	// Existing public API is unchanged.
	assert.Equal(t, sdkKey, rotator.AnchorKey())
	assert.Equal(t, mobileKey, rotator.MobileKey())
	assert.Equal(t, envID, rotator.EnvironmentID())
}

func TestReconcileAnchorOnly(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor})), now)
	require.NotNil(t, result.AnchorChange, "anchor transition from empty to defined is signaled")
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	additions, expirations := r.StepTime(now)

	assert.ElementsMatch(t, []SDKCredential{anchor}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, anchor, r.AnchorKey())
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.AllCredentials())
	assert.Empty(t, r.DeprecatedCredentials())
}

func TestReconcileMultipleSDKKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	now := time.Now()

	result := r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor}).WithSDKKey(SDKKeyParams{Value: other})), now)
	require.NotNil(t, result.AnchorChange)
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	additions, expirations := r.StepTime(now)

	// Both server keys are accepted; only the anchor is primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, other}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, anchor, r.AnchorKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, other}, r.AllCredentials())
	assert.Empty(t, r.DeprecatedCredentials())
}

func TestReconcileDefersAnchorFlipUntilCommit(t *testing.T) {
	r := newTestRotator()
	first := config.SDKKey("first-anchor")
	second := config.SDKKey("second-anchor")
	now := time.Now()

	// Establishing the initial anchor is itself a transition from the undefined key.
	result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: first})), now)
	require.NotNil(t, result.AnchorChange)
	assert.Equal(t, config.SDKKey(""), result.AnchorChange.PreviousAnchor)
	assert.Equal(t, first, result.AnchorChange.NewAnchor)
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	require.Equal(t, first, r.AnchorKey())

	// Re-anchor to a new key while the old one stays valid in a grace period. Reconcile must report
	// the change but leave the pointer on the previous anchor until CommitAnchor is called.
	result = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: second}).
		WithSDKKey(SDKKeyParams{Value: first, Expiry: util.PtrOrNil(now.Add(time.Hour))})), now)
	require.NotNil(t, result.AnchorChange)
	assert.Equal(t, first, result.AnchorChange.PreviousAnchor)
	assert.Equal(t, second, result.AnchorChange.NewAnchor)
	assert.Equal(t, first, r.AnchorKey(), "Reconcile must not flip the anchor before CommitAnchor")

	r.CommitAnchor(result.AnchorChange.NewAnchor)
	assert.Equal(t, second, r.AnchorKey())
}

func TestReconcileWithoutAnchorChangeSignalsNil(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor})), now)
	require.NotNil(t, result.AnchorChange)
	r.CommitAnchor(result.AnchorChange.NewAnchor)

	// Reconciling again with the same anchor is not a transition.
	result = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor})), now)
	assert.Nil(t, result.AnchorChange, "no anchor change when the anchor is unchanged")
	assert.Equal(t, anchor, r.AnchorKey())
}

func TestReconcileMultipleMobileKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	mob1 := config.MobileKey("mob1")
	mob2 := config.MobileKey("mob2")
	now := time.Now()

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor}).WithPrimaryMobileKey(MobileKeyParams{Value: mob1}).WithMobileKey(MobileKeyParams{Value: mob2})), now)
	additions, _ := r.StepTime(now)

	// Every mobile key is accepted; the designated one is the primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, additions)
	assert.Equal(t, mob1, r.MobileKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, r.AllCredentials())
}

func TestReconcileRevokesOmittedKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	mob := config.MobileKey("mob")
	now := time.Now()

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor}).WithSDKKey(SDKKeyParams{Value: other}).WithPrimaryMobileKey(MobileKeyParams{Value: mob})), now)
	r.StepTime(now)

	// Reconciling to just the anchor revokes the omitted server and mobile keys.
	r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: anchor})), now)
	additions, expirations := r.StepTime(now)

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{other, mob}, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.AllCredentials())
}

func TestReconcileAcceptsExpiringKeysAsData(t *testing.T) {
	// Reconcile stores per-key expiry as data on the accepted entry; before that expiry passes, an
	// expiring key is still accepted (it authenticates and appears in AllCredentials) while also
	// being reported as deprecated — accepted, but on its way out. The cleanup ticker (StepTime) only
	// drops it once the expiry elapses — see TestReconcileExpiringKeysAreEvictedByStepTime.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	expiringSDK := config.SDKKey("expiring-sdk")
	mob := config.MobileKey("mob")
	expiringMobile := config.MobileKey("expiring-mob")
	now := time.Unix(1000, 0)

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: expiringSDK, Expiry: util.PtrOrNil(now.Add(time.Hour))}).
			WithPrimaryMobileKey(MobileKeyParams{Value: mob}).
			WithMobileKey(MobileKeyParams{Value: expiringMobile, Expiry: util.PtrOrNil(now.Add(time.Hour))})),
		now)
	additions, expirations := r.StepTime(now)

	assert.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, additions)
	assert.Empty(t, expirations)
	// Every key is accepted (still authenticates)...
	assert.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, r.AllCredentials())
	// ...and the non-anchor SDK key carrying an expiry is also reported as deprecated (being phased
	// out). The expiring mobile key is not: there is no expiringMobileKey status field, so the reconcile
	// path treats it as accepted-only.
	assert.ElementsMatch(t, []SDKCredential{expiringSDK}, r.DeprecatedCredentials())
}

func TestReconcilePrimaryMobileKeyIsAlwaysAccepted(t *testing.T) {
	// Defensive: even if the designated primary mobile key is also listed with a past expiry, it must
	// stay accepted (mirroring the SDK anchor), so AllCredentials never reports a torn-down key.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	mob := config.MobileKey("mob")
	now := time.Unix(1000, 0)

	set := mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: anchor}).
		WithMobileKey(MobileKeyParams{Value: mob, Expiry: util.PtrOrNil(now.Add(-time.Hour))}). // already expired in the payload...
		WithPrimaryMobileKey(MobileKeyParams{Value: mob}))                                      // ...but designated as the primary
	r.Reconcile(set, now)
	r.StepTime(now)

	assert.Equal(t, mob, r.MobileKey())
	assert.Contains(t, r.AllCredentials(), SDKCredential(mob))
	_, accepted := r.acceptedMobileKeys[mob]
	assert.True(t, accepted, "the primary mobile key must remain in the accepted set")
}

func TestReconcileExpiringKeysAreEvictedByStepTime(t *testing.T) {
	// End-to-end on the reconcile path: a reconcile records per-key expiry as data on the accepted
	// entry, and the cleanup ticker (StepTime) later drops both the expiring SDK key and the expiring
	// mobile key once their expiry elapses. The anchor and primary mobile key carry no expiry and survive.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	expiringSDK := config.SDKKey("expiring-sdk")
	mob := config.MobileKey("mob")
	expiringMobile := config.MobileKey("expiring-mob")
	now := time.Unix(1000, 0)
	expiry := now.Add(time.Hour)

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: expiringSDK, Expiry: util.PtrOrNil(expiry)}).
			WithPrimaryMobileKey(MobileKeyParams{Value: mob}).
			WithMobileKey(MobileKeyParams{Value: expiringMobile, Expiry: util.PtrOrNil(expiry)})),
		now)
	additions, expirations := r.StepTime(now)
	require.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, additions)
	require.Empty(t, expirations)

	// At the exact expiry, expiry is strict (now must be strictly after), so nothing is dropped yet.
	additions, expirations = r.StepTime(expiry)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	// One moment past the expiry: both expiring keys are evicted; anchor and primary mobile survive.
	additions, expirations = r.StepTime(expiry.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{expiringSDK, expiringMobile}, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor, mob}, r.AllCredentials())
	assert.NotContains(t, r.AllCredentials(), SDKCredential(expiringSDK))
	assert.NotContains(t, r.AllCredentials(), SDKCredential(expiringMobile))
}

func TestReconcileAlreadyExpiredKeyIsIgnoredOnAdd(t *testing.T) {
	// An entry in the reconcile payload whose expiry is already in the past is treated as absent —
	// the reconcile path filters it before calling reconcileAcceptedKeys, so it is never added.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	staleKey := config.SDKKey("stale")
	now := time.Unix(2000, 0)
	alreadyExpired := now.Add(-time.Hour)

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: staleKey, Expiry: util.PtrOrNil(alreadyExpired)})),
		now)
	additions, expirations := r.StepTime(now)

	// Only the anchor is added; the stale key is never accepted.
	assert.ElementsMatch(t, []SDKCredential{anchor}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.AllCredentials())
}

func TestReconcileDeExpiryRestoresKey(t *testing.T) {
	// When a key was accepted with a future expiry and a subsequent reconcile removes that expiry
	// (de-expiry), the key becomes permanent: the cleanup ticker will no longer drop it, and it is
	// no longer reported as deprecated.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	key := config.SDKKey("other")
	now := time.Unix(1000, 0)
	expiry := now.Add(time.Hour)

	// First reconcile: key is accepted with a future expiry.
	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: key, Expiry: util.PtrOrNil(expiry)})),
		now)
	r.StepTime(now)
	require.ElementsMatch(t, []SDKCredential{key}, r.DeprecatedCredentials())

	// Second reconcile: same key, no expiry (de-expiry).
	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: key})),
		now)
	r.StepTime(now)

	// The key is still accepted and permanent: no longer deprecated, not evicted by StepTime.
	assert.Contains(t, r.AllCredentials(), SDKCredential(key))
	assert.Empty(t, r.DeprecatedCredentials())

	additions, expirations := r.StepTime(expiry.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.Contains(t, r.AllCredentials(), SDKCredential(key))
}

// TestAcceptedKeys verifies that AcceptedKeys returns the full accepted set — every server and mobile
// key, including the anchor and primary mobile key — grouped by kind with identifier and expiry
// populated, plus the anchor and primary-mobile designations.
func TestAcceptedKeys(t *testing.T) {
	t.Run("single anchor plus primary mobile", func(t *testing.T) {
		r := newTestRotator()
		result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
			WithPrimaryMobileKey(MobileKeyParams{Value: "mob-primary", Key: util.PtrOrNil("mob-1")})), time.Unix(0, 0))
		require.NotNil(t, result.AnchorChange)
		r.CommitAnchor(result.AnchorChange.NewAnchor)

		set := r.AcceptedKeys()
		require.Len(t, set.Server, 1)
		require.Len(t, set.Mobile, 1)
		assert.Equal(t, config.SDKKey("sdk-anchor"), set.Anchor)
		assert.Equal(t, config.MobileKey("mob-primary"), set.PrimaryMobile)

		anchor, ok := set.Server["sdk-anchor"]
		require.True(t, ok)
		require.NotNil(t, anchor.Key)
		assert.Equal(t, "default", *anchor.Key)
		assert.Nil(t, anchor.Expiry)

		mob, ok := set.Mobile["mob-primary"]
		require.True(t, ok)
		require.NotNil(t, mob.Key)
		assert.Equal(t, "mob-1", *mob.Key)
	})

	t.Run("multiple keys include the anchor; expiry populated", func(t *testing.T) {
		r := newTestRotator()
		expiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: "sdk-anchor", Key: util.PtrOrNil("default")}).
			WithSDKKey(SDKKeyParams{Value: "sdk-b", Key: util.PtrOrNil("b-service")}).
			WithSDKKey(SDKKeyParams{Value: "sdk-old", Key: util.PtrOrNil("old-key"), Expiry: util.PtrOrNil(expiry)}).
			WithPrimaryMobileKey(MobileKeyParams{Value: "mob-primary"})), time.Unix(0, 0))
		require.NotNil(t, result.AnchorChange)
		r.CommitAnchor(result.AnchorChange.NewAnchor)

		set := r.AcceptedKeys()
		require.Len(t, set.Server, 3) // anchor + sdk-b + sdk-old
		require.Len(t, set.Mobile, 1)
		_, ok := set.Server["sdk-anchor"]
		assert.True(t, ok, "anchor must be present in the full set")

		old, ok := set.Server["sdk-old"]
		require.True(t, ok)
		require.NotNil(t, old.Expiry)
		assert.Equal(t, expiry, *old.Expiry)

		// A key with no identifier (the primary mobile here) carries a nil Key.
		mob, ok := set.Mobile["mob-primary"]
		require.True(t, ok)
		assert.Nil(t, mob.Key)
	})
}

// TestReconcileClearsStaleKeyIdentifier verifies that when a later reconcile carries no identifier for
// a key that previously had one (e.g. an old-format payload after a new-format one), the rotator
// clears the stale identifier rather than retaining it — so /status never shows an identifier the
// current credential set no longer carries.
func TestReconcileClearsStaleKeyIdentifier(t *testing.T) {
	r := newTestRotator()
	now := time.Unix(0, 0)

	// First reconcile: sdk-b carries the identifier "b-service".
	r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: "sdk-anchor"}).
		WithSDKKey(SDKKeyParams{Value: "sdk-b", Key: util.PtrOrNil("b-service")})), now)

	b, ok := r.AcceptedKeys().Server["sdk-b"]
	require.True(t, ok)
	require.NotNil(t, b.Key)
	assert.Equal(t, "b-service", *b.Key)

	// Second reconcile: same credential value, but no identifier this time.
	r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: "sdk-anchor"}).
		WithSDKKey(SDKKeyParams{Value: "sdk-b"})), now)

	b, ok = r.AcceptedKeys().Server["sdk-b"]
	require.True(t, ok)
	assert.Nil(t, b.Key, "identifier must be cleared when the new payload carries none")
}
