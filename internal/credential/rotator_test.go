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

	// The anchor is stripped from additions — the synchronous re-anchor sequence owns its setup.
	assert.Empty(t, additions)
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

	// Both server keys are accepted; only the non-anchor server key is in additions (the anchor is
	// owned by the synchronous re-anchor sequence in env_context_impl).
	assert.ElementsMatch(t, []SDKCredential{other}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, anchor, r.AnchorKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, other}, r.AllCredentials())
	assert.Empty(t, r.DeprecatedCredentials())
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

	// Every mobile key is accepted; the anchor is owned by the synchronous re-anchor (stripped from
	// additions). The designated primary mobile key and the other mobile key remain in additions.
	assert.ElementsMatch(t, []SDKCredential{mob1, mob2}, additions)
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

	// Anchor is stripped from additions (owned by the synchronous re-anchor); other keys flow through.
	assert.ElementsMatch(t, []SDKCredential{expiringSDK, mob, expiringMobile}, additions)
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
	// Anchor is stripped from additions (owned by the synchronous re-anchor); other keys flow through.
	require.ElementsMatch(t, []SDKCredential{expiringSDK, mob, expiringMobile}, additions)
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

	result := r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: staleKey, Expiry: util.PtrOrNil(alreadyExpired)})),
		now)
	require.NotNil(t, result.AnchorChange)
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	additions, expirations := r.StepTime(now)

	// The fresh anchor is stripped from additions (the synchronous re-anchor sequence owns its setup),
	// and the already-expired stale key is never accepted — so nothing is added.
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.AllCredentials())
}

func TestReconcileExpiryBoundaryIsStrictlyAfter(t *testing.T) {
	// The reconcile-side filter that treats an already-expired key as absent must honor the same
	// strictly-after contract as StepTime (see the doc comment on StepTime): a key whose expiry lands
	// exactly on `now` is still accepted by Reconcile, and only becomes absent once `now` is one instant
	// past the expiry.
	anchor := config.SDKKey("anchor")
	staleKey := config.SDKKey("stale")
	expiry := time.Unix(2000, 0)

	atBoundary := newTestRotator()
	atBoundary.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: staleKey, Expiry: util.PtrOrNil(expiry)})),
		expiry)
	assert.Contains(t, atBoundary.AllCredentials(), SDKCredential(staleKey), "a key expiring exactly at now is still accepted by Reconcile")

	pastBoundary := newTestRotator()
	pastBoundary.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: staleKey, Expiry: util.PtrOrNil(expiry)})),
		expiry.Add(1*time.Millisecond))
	assert.NotContains(t, pastBoundary.AllCredentials(), SDKCredential(staleKey), "a key one instant past its expiry is treated as absent by Reconcile")
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

func TestReconcileAnchorChangeToPreviouslyAcceptedKey(t *testing.T) {
	// When the anchor moves to a key that was already accepted (a non-anchor server key promoted to
	// anchor), the AnchorChange reports NewAnchorPreviouslyAccepted == true, and the key is not queued
	// as a new addition (it was already accepted).
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	now := time.Now()

	result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: anchor}).
		WithSDKKey(SDKKeyParams{Value: other})), now)
	require.NotNil(t, result.AnchorChange)
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	r.StepTime(now)

	// Move the anchor to `other`, keeping the old anchor accepted.
	result = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: other}).
		WithSDKKey(SDKKeyParams{Value: anchor})), now)
	require.NotNil(t, result.AnchorChange)
	assert.Equal(t, anchor, result.AnchorChange.PreviousAnchor)
	assert.Equal(t, other, result.AnchorChange.NewAnchor)
	assert.True(t, result.AnchorChange.NewAnchorPreviouslyAccepted, "other was already in the accepted set")

	additions, _ := r.StepTime(now)
	assert.NotContains(t, additions, SDKCredential(other), "an already-accepted new anchor is not a fresh addition")
}

func TestReconcileMobilePrimaryRepointToAlreadyAcceptedKey(t *testing.T) {
	// Switching the primary mobile key to a key that is already accepted must be signaled via
	// MobilePrimaryRepoint, because addCredential's gate won't fire for it (it's not in additions).
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	mob1 := config.MobileKey("mob1")
	mob2 := config.MobileKey("mob2")
	now := time.Now()

	result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: anchor}).
		WithPrimaryMobileKey(MobileKeyParams{Value: mob1}).
		WithMobileKey(MobileKeyParams{Value: mob2})), now)
	require.NotNil(t, result.AnchorChange)
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	r.StepTime(now)

	// Make mob2 (already accepted) the primary. It is not a new addition, so it must be signaled.
	result = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: anchor}).
		WithPrimaryMobileKey(MobileKeyParams{Value: mob2}).
		WithMobileKey(MobileKeyParams{Value: mob1})), now)
	assert.Nil(t, result.AnchorChange, "the anchor did not change")
	require.NotNil(t, result.MobilePrimaryRepoint, "an already-accepted new primary mobile key must be signaled")
	assert.Equal(t, mob2, *result.MobilePrimaryRepoint)
}

func TestReconcileMobilePrimaryToNewKeyDoesNotSignalRepoint(t *testing.T) {
	// When the new primary mobile key was NOT already accepted, it arrives via the additions list, so
	// addCredential's gate handles the ReplaceCredential and MobilePrimaryRepoint stays nil.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	mob1 := config.MobileKey("mob1")
	mob2 := config.MobileKey("mob2")
	now := time.Now()

	result := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: anchor}).
		WithPrimaryMobileKey(MobileKeyParams{Value: mob1})), now)
	require.NotNil(t, result.AnchorChange)
	r.CommitAnchor(result.AnchorChange.NewAnchor)
	r.StepTime(now)

	result = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: anchor}).
		WithPrimaryMobileKey(MobileKeyParams{Value: mob2})), now)
	assert.Nil(t, result.MobilePrimaryRepoint, "a brand-new primary mobile key is handled via additions, not the repoint signal")

	additions, _ := r.StepTime(now)
	assert.Contains(t, additions, SDKCredential(mob2), "a brand-new primary mobile key is queued as an addition")
}

func TestRevertAnchorChangeReadmitsRevokedPreviousAnchor(t *testing.T) {
	// When the previous anchor was immediately revoked in the same reconcile (dropped from the accepted
	// set) and the re-anchor rolled back, RevertAnchorChange re-admits it and drops the failed new key.
	r := newTestRotator()
	keyA := config.SDKKey("keyA")
	keyB := config.SDKKey("keyB")
	now := time.Now()

	res := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: keyA})), now)
	require.NotNil(t, res.AnchorChange)
	r.CommitAnchor(res.AnchorChange.NewAnchor)
	r.StepTime(now)

	// Move the anchor to keyB, omitting keyA entirely (immediate revocation). Reconcile drops keyA.
	res = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: keyB})), now)
	require.NotNil(t, res.AnchorChange)
	require.False(t, res.AnchorChange.NewAnchorPreviouslyAccepted)

	// Simulate the rollback: the caller did NOT CommitAnchor, so the pointer still names keyA.
	r.RevertAnchorChange(*res.AnchorChange)

	assert.Equal(t, keyA, r.AnchorKey())
	creds := r.AllCredentials()
	assert.Contains(t, creds, SDKCredential(keyA), "previous anchor re-admitted")
	assert.NotContains(t, creds, SDKCredential(keyB), "failed new anchor dropped")
}

func TestRevertAnchorChangeLeavesGraceDemotedPreviousAnchorUntouched(t *testing.T) {
	// When the previous anchor was demoted with a grace expiry (still accepted) rather than revoked,
	// RevertAnchorChange leaves it — and its expiry — untouched (it does not re-admit it as permanent).
	// That is safe because StepTime refuses to expire the current anchor even when its entry carries a
	// stale expiry (see TestStepTimeDoesNotExpireCurrentAnchor); RevertAnchorChange itself does not need
	// to clear the expiry.
	r := newTestRotator()
	keyA := config.SDKKey("keyA")
	keyB := config.SDKKey("keyB")
	now := time.Unix(1000, 0)
	expiry := now.Add(time.Hour)

	res := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: keyA})), now)
	require.NotNil(t, res.AnchorChange)
	r.CommitAnchor(res.AnchorChange.NewAnchor)
	r.StepTime(now)

	// Move the anchor to keyB while keeping keyA accepted with a grace expiry.
	res = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: keyB}).
		WithSDKKey(SDKKeyParams{Value: keyA, Expiry: util.PtrOrNil(expiry)})), now)
	require.NotNil(t, res.AnchorChange)

	r.RevertAnchorChange(*res.AnchorChange)

	set := r.AcceptedKeys()
	kaInfo, ok := set.Server[keyA]
	require.True(t, ok, "grace-demoted previous anchor stays accepted")
	require.NotNil(t, kaInfo.Expiry, "RevertAnchorChange must not wipe the grace expiry / make it permanent")
	assert.Equal(t, expiry, *kaInfo.Expiry)
	_, keyBAccepted := set.Server[keyB]
	assert.False(t, keyBAccepted, "failed new anchor dropped")
}

// TestStepTimeDoesNotExpireCurrentAnchor is the rotator-level guard for the re-anchor-rollback outage:
// even if the current anchor's accepted entry carries an expiry (as it does after a grace-demotion
// re-anchor rolls back before CommitAnchor), StepTime must not expire it. Non-vacuous: without the
// anchor guard in StepTime, this returns keyA in expirations and drops it from the accepted set.
func TestStepTimeDoesNotExpireCurrentAnchor(t *testing.T) {
	r := newTestRotator()
	keyA := config.SDKKey("keyA")
	keyB := config.SDKKey("keyB")
	now := time.Unix(1000, 0)
	expiry := now.Add(time.Hour)

	res := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: keyA})), now)
	require.NotNil(t, res.AnchorChange)
	r.CommitAnchor(res.AnchorChange.NewAnchor)
	r.StepTime(now)

	// Re-anchor A->B with A grace-demoted; then roll back (never CommitAnchor(keyB), and RevertAnchorChange
	// leaves A's expiry). A is still the anchor but now carries a grace expiry.
	res = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: keyB}).
		WithSDKKey(SDKKeyParams{Value: keyA, Expiry: util.PtrOrNil(expiry)})), now)
	require.NotNil(t, res.AnchorChange)
	r.RevertAnchorChange(*res.AnchorChange)
	require.Equal(t, keyA, r.AnchorKey(), "anchor stayed on keyA after rollback")

	// The cleanup ticker fires past the grace deadline. The anchor must survive.
	_, expirations := r.StepTime(expiry.Add(time.Minute))

	assert.NotContains(t, expirations, SDKCredential(keyA), "StepTime must not expire the current anchor")
	assert.Contains(t, r.AllCredentials(), SDKCredential(keyA), "the anchor stays accepted")
	assert.Equal(t, keyA, r.AnchorKey())
}

// TestStepTimeExpiresDemotedFormerAnchorAfterSuccessfulReanchor pins the narrowness of the anchor guard:
// it protects only the CURRENT anchor. After a SUCCESSFUL re-anchor A->B (CommitAnchor moved the pointer
// to B), the grace-demoted former anchor A is no longer r.anchorKey, so StepTime expires it normally once
// its grace window passes -- the old client must not be pinned alive forever.
func TestStepTimeExpiresDemotedFormerAnchorAfterSuccessfulReanchor(t *testing.T) {
	r := newTestRotator()
	keyA := config.SDKKey("keyA")
	keyB := config.SDKKey("keyB")
	now := time.Unix(1000, 0)
	expiry := now.Add(time.Hour)

	res := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: keyA})), now)
	require.NotNil(t, res.AnchorChange)
	r.CommitAnchor(res.AnchorChange.NewAnchor)
	r.StepTime(now)

	// Successful re-anchor A->B: the pointer moves to B; A is grace-demoted.
	res = r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().
		WithAnchor(SDKKeyParams{Value: keyB}).
		WithSDKKey(SDKKeyParams{Value: keyA, Expiry: util.PtrOrNil(expiry)})), now)
	require.NotNil(t, res.AnchorChange)
	r.CommitAnchor(res.AnchorChange.NewAnchor)
	require.Equal(t, keyB, r.AnchorKey())

	// Past A's grace window: A (a non-anchor demoted key now) expires; B (the anchor) survives.
	_, expirations := r.StepTime(expiry.Add(time.Minute))
	assert.Contains(t, expirations, SDKCredential(keyA), "the demoted former anchor expires normally")
	assert.NotContains(t, r.AllCredentials(), SDKCredential(keyA), "and is dropped from the accepted set")
	assert.Contains(t, r.AllCredentials(), SDKCredential(keyB), "the new anchor survives")
	assert.Equal(t, keyB, r.AnchorKey())
}

func TestRevertAnchorChangeDoesNotAdmitUndefinedPreviousAnchor(t *testing.T) {
	// When an env gains its first SDK key, the AnchorChange's previous anchor is the empty (undefined)
	// key. A rollback must not insert that empty key into the accepted set.
	r := newTestRotator()
	keyB := config.SDKKey("keyB")
	now := time.Now()

	res := r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithAnchor(SDKKeyParams{Value: keyB})), now)
	require.NotNil(t, res.AnchorChange)
	require.False(t, res.AnchorChange.PreviousAnchor.Defined(), "the first SDK key has an undefined previous anchor")

	// Simulate a failed build / rollback.
	r.RevertAnchorChange(*res.AnchorChange)

	for _, cred := range r.AllCredentials() {
		if sdkKey, ok := cred.(config.SDKKey); ok {
			assert.True(t, sdkKey.Defined(), "revert must not insert an undefined SDK key into the accepted set")
		}
	}
	assert.NotContains(t, r.AllCredentials(), SDKCredential(keyB), "failed new anchor dropped")
}

func TestIsAcceptedMatchesAllCredentials(t *testing.T) {
	// IsAccepted is the membership form of AllCredentials, so the two must never disagree -- including on
	// keys carrying a future expiry, which still authenticate until the cleanup ticker drops them.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	expiringSDK := config.SDKKey("expiring-sdk")
	mob := config.MobileKey("mob")
	expiringMobile := config.MobileKey("expiring-mob")
	envID := config.EnvironmentID("env-id")
	now := time.Unix(1000, 0)

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: expiringSDK, Expiry: util.PtrOrNil(now.Add(time.Hour))}).
			WithPrimaryMobileKey(MobileKeyParams{Value: mob}).
			WithMobileKey(MobileKeyParams{Value: expiringMobile, Expiry: util.PtrOrNil(now.Add(time.Hour))}).
			WithEnvironmentID(envID)),
		now)
	r.StepTime(now)

	all := r.AllCredentials()
	require.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile, envID}, all)
	for _, cred := range all {
		assert.True(t, r.IsAccepted(cred), "AllCredentials reported %s, so IsAccepted must agree", cred.Masked())
	}

	// Values of each tracked kind that were never accepted.
	assert.False(t, r.IsAccepted(config.SDKKey("unknown-sdk")))
	assert.False(t, r.IsAccepted(config.MobileKey("unknown-mob")))
	assert.False(t, r.IsAccepted(config.EnvironmentID("unknown-env")))

	// Undefined values are never accepted -- the rotator only ever holds defined credentials.
	assert.False(t, r.IsAccepted(config.SDKKey("")))
	assert.False(t, r.IsAccepted(config.MobileKey("")))
	assert.False(t, r.IsAccepted(config.EnvironmentID("")))

	// A credential kind the rotator does not track is never accepted, even when defined.
	assert.False(t, r.IsAccepted(config.AutoConfigKey("auto-config-key")))
}

func TestIsAcceptedFollowsRevocation(t *testing.T) {
	// Reconciling to a set that omits a key revokes it, and IsAccepted must report it as such while
	// leaving the retained credentials accepted. This is the predicate the stream handler relies on to
	// reject a credential revoked after the request authenticated.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	mob := config.MobileKey("mob")
	otherMob := config.MobileKey("other-mob")
	now := time.Unix(1000, 0)

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithSDKKey(SDKKeyParams{Value: other}).
			WithPrimaryMobileKey(MobileKeyParams{Value: mob}).
			WithMobileKey(MobileKeyParams{Value: otherMob})),
		now)
	r.StepTime(now)
	require.True(t, r.IsAccepted(other))
	require.True(t, r.IsAccepted(otherMob))

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithAnchor(SDKKeyParams{Value: anchor}).
			WithPrimaryMobileKey(MobileKeyParams{Value: mob})),
		now)
	r.StepTime(now)

	assert.False(t, r.IsAccepted(other), "revoked SDK key")
	assert.False(t, r.IsAccepted(otherMob), "revoked mobile key")
	assert.True(t, r.IsAccepted(anchor), "anchor is retained")
	assert.True(t, r.IsAccepted(mob), "primary mobile key is retained")
}

func TestIsAcceptedFalseOnceExpiryElapses(t *testing.T) {
	// A key carrying a future expiry stays accepted until the cleanup ticker drops it, then stops being
	// accepted -- without any further reconcile.
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
	r.StepTime(now)
	require.True(t, r.IsAccepted(expiringSDK), "accepted before its expiry elapses")
	require.True(t, r.IsAccepted(expiringMobile), "accepted before its expiry elapses")

	r.StepTime(expiry.Add(time.Second))

	assert.False(t, r.IsAccepted(expiringSDK), "dropped by the cleanup ticker")
	assert.False(t, r.IsAccepted(expiringMobile), "dropped by the cleanup ticker")
	assert.True(t, r.IsAccepted(anchor), "the anchor never expires")
	assert.True(t, r.IsAccepted(mob), "the primary mobile key is permanent")
}
