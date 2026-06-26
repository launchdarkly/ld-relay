package credential

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/ld-relay/v8/config"
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
		assert.Nil(t, info.expiry, "a key initialized without expiry should have nil expiry in acceptedKeyInfo")
	}

	// Verify accepted mobile key set: one entry, no expiry.
	assert.Len(t, rotator.acceptedMobileKeys, 1)
	if info, ok := rotator.acceptedMobileKeys[mobileKey]; assert.True(t, ok, "acceptedMobileKeys should contain the initialized mobile key") {
		assert.Nil(t, info.expiry, "a key initialized without expiry should have nil expiry in acceptedKeyInfo")
	}

	// Existing public API is unchanged.
	assert.Equal(t, sdkKey, rotator.SDKKey())
	assert.Equal(t, mobileKey, rotator.MobileKey())
	assert.Equal(t, envID, rotator.EnvironmentID())
}

func TestReconcileAnchorOnly(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor)), now)
	additions, expirations := r.StepTime(now)

	assert.ElementsMatch(t, []SDKCredential{anchor}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, anchor, r.SDKKey())
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
	assert.Empty(t, r.DeprecatedCredentials())
}

func TestReconcileMultipleSDKKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	now := time.Now()

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithSDKKey(other)), now)
	additions, expirations := r.StepTime(now)

	// Both server keys are accepted; only the anchor is primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, other}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, anchor, r.SDKKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, other}, r.PrimaryCredentials())
	assert.Empty(t, r.DeprecatedCredentials())
}

func TestReconcileMultipleMobileKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	mob1 := config.MobileKey("mob1")
	mob2 := config.MobileKey("mob2")
	now := time.Now()

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithPrimaryMobileKey(mob1).WithMobileKey(mob2)), now)
	additions, _ := r.StepTime(now)

	// Every mobile key is accepted; the designated one is the primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, additions)
	assert.Equal(t, mob1, r.MobileKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, r.PrimaryCredentials())
}

func TestReconcileRevokesOmittedKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	mob := config.MobileKey("mob")
	now := time.Now()

	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithSDKKey(other).WithPrimaryMobileKey(mob)), now)
	r.StepTime(now)

	// Reconciling to just the anchor revokes the omitted server and mobile keys.
	r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor)), now)
	additions, expirations := r.StepTime(now)

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{other, mob}, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}

func TestReconcileAcceptsExpiringKeysAsData(t *testing.T) {
	// Reconcile stores per-key expiry as data on the accepted entry; before that expiry passes, an
	// expiring key is still accepted (it authenticates and appears in PrimaryCredentials) while also
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
			WithPrimarySDKKey(anchor).
			WithExpiringSDKKey(expiringSDK, now.Add(time.Hour)).
			WithPrimaryMobileKey(mob).
			WithExpiringMobileKey(expiringMobile, now.Add(time.Hour))),
		now)
	additions, expirations := r.StepTime(now)

	assert.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, additions)
	assert.Empty(t, expirations)
	// Every key is accepted (still authenticates)...
	assert.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, r.PrimaryCredentials())
	// ...and the non-anchor SDK key carrying an expiry is also reported as deprecated (being phased
	// out). The expiring mobile key is not: there is no expiringMobileKey status field, so the reconcile
	// path treats it as accepted-only.
	assert.ElementsMatch(t, []SDKCredential{expiringSDK}, r.DeprecatedCredentials())
}

func TestReconcilePrimaryMobileKeyIsAlwaysAccepted(t *testing.T) {
	// Defensive: even if the designated primary mobile key is also listed with a past expiry, it must
	// stay accepted (mirroring the SDK anchor), so PrimaryCredentials never reports a torn-down key.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	mob := config.MobileKey("mob")
	now := time.Unix(1000, 0)

	set := mustBuild(t, NewAcceptedSetBuilder().
		WithPrimarySDKKey(anchor).
		WithExpiringMobileKey(mob, now.Add(-time.Hour)). // already expired in the payload...
		WithPrimaryMobileKey(mob))                       // ...but designated as the primary
	r.Reconcile(set, now)
	r.StepTime(now)

	assert.Equal(t, mob, r.MobileKey())
	assert.Contains(t, r.PrimaryCredentials(), SDKCredential(mob))
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
			WithPrimarySDKKey(anchor).
			WithExpiringSDKKey(expiringSDK, expiry).
			WithPrimaryMobileKey(mob).
			WithExpiringMobileKey(expiringMobile, expiry)),
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
	assert.ElementsMatch(t, []SDKCredential{anchor, mob}, r.PrimaryCredentials())
	assert.NotContains(t, r.PrimaryCredentials(), SDKCredential(expiringSDK))
	assert.NotContains(t, r.PrimaryCredentials(), SDKCredential(expiringMobile))
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
			WithPrimarySDKKey(anchor).
			WithExpiringSDKKey(staleKey, alreadyExpired)),
		now)
	additions, expirations := r.StepTime(now)

	// Only the anchor is added; the stale key is never accepted.
	assert.ElementsMatch(t, []SDKCredential{anchor}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
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
			WithPrimarySDKKey(anchor).
			WithExpiringSDKKey(key, expiry)),
		now)
	r.StepTime(now)
	require.ElementsMatch(t, []SDKCredential{key}, r.DeprecatedCredentials())

	// Second reconcile: same key, no expiry (de-expiry).
	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithPrimarySDKKey(anchor).
			WithSDKKey(key)),
		now)
	r.StepTime(now)

	// The key is still accepted and permanent: no longer deprecated, not evicted by StepTime.
	assert.Contains(t, r.PrimaryCredentials(), SDKCredential(key))
	assert.Empty(t, r.DeprecatedCredentials())

	additions, expirations := r.StepTime(expiry.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.Contains(t, r.PrimaryCredentials(), SDKCredential(key))
}
