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

func TestMalformedCredentialSetErrorMessage(t *testing.T) {
	// A nil anchor reports "missing" rather than dereferencing a nil credential.
	assert.Equal(t, "malformed credential set: anchor SDK key is missing",
		(&MalformedCredentialSetError{Anchor: nil}).Error())

	// A defined anchor is masked in the message.
	assert.Contains(t, (&MalformedCredentialSetError{Anchor: config.SDKKey("sdk-abcd1234")}).Error(),
		"...1234")
}

func mustBuild(t *testing.T, b *AcceptedSetBuilder) AcceptedSet {
	t.Helper()
	set, err := b.Build()
	require.NoError(t, err)
	return set
}

func TestReconcileAnchorOnly(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor)), now))
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

	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithSDKKey(other)), now))
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

	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithPrimaryMobileKey(mob1).WithMobileKey(mob2)), now))
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

	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithSDKKey(other).WithPrimaryMobileKey(mob)), now))
	r.StepTime(now)

	// Reconciling to just the anchor revokes the omitted server and mobile keys.
	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor)), now))
	additions, expirations := r.StepTime(now)

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{other, mob}, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}

func TestReconcileAcceptsExpiringKeysAsData(t *testing.T) {
	// The foundation stores per-key expiry but does not yet act on it (no grace-period deprecation,
	// no cleanup ticker — those are handled separately). An expiring key is simply accepted.
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	expiringSDK := config.SDKKey("expiring-sdk")
	mob := config.MobileKey("mob")
	expiringMobile := config.MobileKey("expiring-mob")
	now := time.Unix(1000, 0)

	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithPrimarySDKKey(anchor).
			WithExpiringSDKKey(expiringSDK, now.Add(time.Hour)).
			WithPrimaryMobileKey(mob).
			WithExpiringMobileKey(expiringMobile, now.Add(time.Hour))),
		now))
	additions, expirations := r.StepTime(now)

	assert.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, additions)
	assert.Empty(t, expirations)
	// All keys are accepted and non-deprecated in the foundation.
	assert.ElementsMatch(t, []SDKCredential{anchor, expiringSDK, mob, expiringMobile}, r.PrimaryCredentials())
	assert.Empty(t, r.DeprecatedCredentials())
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
	require.NoError(t, r.Reconcile(set, now))
	r.StepTime(now)

	assert.Equal(t, mob, r.MobileKey())
	assert.Contains(t, r.PrimaryCredentials(), SDKCredential(mob))
	_, accepted := r.acceptedMobileKeys[mob]
	assert.True(t, accepted, "the primary mobile key must remain in the accepted set")
}

func TestReconcileRejectsSetWithoutAnchor(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor)), now))
	r.StepTime(now)

	// A set with no designated anchor (e.g. the zero value) is malformed; Reconcile makes no changes.
	var malformed *MalformedCredentialSetError
	require.ErrorAs(t, r.Reconcile(AcceptedSet{}, now), &malformed)
	additions, expirations := r.StepTime(now)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}
