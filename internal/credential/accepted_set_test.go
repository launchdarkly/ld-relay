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
	// A set with no SDK key is a caller error, not a silent no-op.
	_, err := NewAcceptedSetBuilder().
		WithMobileKey(config.MobileKey("mob")).
		WithEnvironmentID(config.EnvironmentID("env")).
		Build()
	require.ErrorIs(t, err, errAcceptedSetMissingSDKKey)

	var malformed *MalformedCredentialSetError

	// An SDK key with no designated anchor is malformed.
	_, err = NewAcceptedSetBuilder().WithSDKKey(config.SDKKey("sdk")).Build()
	require.ErrorAs(t, err, &malformed)

	// A designated anchor that isn't among the SDK keys is malformed.
	_, err = NewAcceptedSetBuilder().
		WithSDKKey(config.SDKKey("sdk")).
		WithAnchorSDKKey(config.SDKKey("other")).
		Build()
	require.ErrorAs(t, err, &malformed)
	assert.Equal(t, config.SDKKey("other"), malformed.Anchor)

	// With an SDK key designated as the anchor, Build succeeds.
	set, err := NewAcceptedSetBuilder().
		WithSDKKey(config.SDKKey("sdk")).
		WithAnchorSDKKey(config.SDKKey("sdk")).
		Build()
	require.NoError(t, err)
	assert.True(t, set.hasSDKKey(config.SDKKey("sdk")))
	assert.Equal(t, config.SDKKey("sdk"), set.primarySdkKey)
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

	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithAnchorSDKKey(anchor)), now))
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
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithSDKKey(other).WithAnchorSDKKey(anchor)), now))
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
		mustBuild(t, NewAcceptedSetBuilder().
			WithSDKKey(anchor).WithAnchorSDKKey(anchor).
			WithMobileKey(mob1).WithMobileKey(mob2).WithPrimaryMobileKey(mob1)), now))
	additions, _ := r.StepTime(now)

	// Every mobile key is accepted; the designated one is the primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, additions)
	assert.Equal(t, mob1, r.MobileKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, r.PrimaryCredentials())
}

func TestReconcileReanchorToAlreadyAcceptedKeyRequeuesAddition(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	now := time.Now()

	// Accept both keys with anchor as the primary; other is accepted as a non-anchor key.
	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithSDKKey(other).WithAnchorSDKKey(anchor)), now))
	additions, _ := r.StepTime(now)
	assert.ElementsMatch(t, []SDKCredential{anchor, other}, additions)

	// Re-anchor onto other, which is already accepted. It must be re-queued as an addition so the
	// caller runs the anchor-only setup (upstream client start, event-forwarding swap) for it —
	// re-anchoring onto an existing non-anchor key is the backend's default-rotation sequence.
	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithSDKKey(other).WithAnchorSDKKey(other)), now))
	additions, expirations := r.StepTime(now)
	assert.ElementsMatch(t, []SDKCredential{other}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, other, r.SDKKey())

	// Reconciling again with the same anchor is a no-op: the anchor is unchanged, so it is not
	// re-queued (which would redundantly restart the upstream client).
	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithSDKKey(other).WithAnchorSDKKey(other)), now))
	additions, expirations = r.StepTime(now)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
}

func TestReconcileMalformedAnchorPreservesState(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithAnchorSDKKey(anchor)), now))
	r.StepTime(now)

	var malformed *MalformedCredentialSetError

	// A set whose anchor is not among its SDK keys is rejected at Build, before it can reach Reconcile.
	_, err := NewAcceptedSetBuilder().
		WithSDKKey(config.SDKKey("other")).
		WithAnchorSDKKey(config.SDKKey("not-in-set")).
		Build()
	require.ErrorAs(t, err, &malformed)

	// Reconcile defends against a set with no anchor (e.g. the zero value): it makes no changes.
	require.ErrorAs(t, r.Reconcile(AcceptedSet{}, now), &malformed)
	additions, expirations := r.StepTime(now)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}

func TestReconcileRevokesOmittedKeys(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	other := config.SDKKey("other")
	mob := config.MobileKey("mob")
	now := time.Now()

	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithSDKKey(anchor).WithSDKKey(other).WithAnchorSDKKey(anchor).
			WithMobileKey(mob).WithPrimaryMobileKey(mob)), now))
	r.StepTime(now)

	// Reconciling to just the anchor revokes the omitted server and mobile keys.
	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithAnchorSDKKey(anchor)), now))
	additions, expirations := r.StepTime(now)

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{other, mob}, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}

func TestReconcileExpiringMobileKey(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	permanent := config.MobileKey("mob-permanent")
	expiring := config.MobileKey("mob-expiring")
	start := time.Unix(1000, 0)

	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().
			WithSDKKey(anchor).WithAnchorSDKKey(anchor).
			WithMobileKey(permanent).WithPrimaryMobileKey(permanent).
			WithExpiringMobileKey(expiring, start.Add(time.Hour))),
		start))
	additions, expirations := r.StepTime(start)

	// Both mobile keys are accepted (and added); the designated one is the primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, permanent, expiring}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, permanent, r.MobileKey())

	// The expiring mobile key is recorded for the cleanup ticker (which drops it — owned by T1.c),
	// while still being accepted until then. The permanent key is not deprecated.
	_, expiringDeprecated := r.deprecatedMobileKeys[expiring]
	_, expiringAccepted := r.acceptedMobileKeys[expiring]
	_, permanentDeprecated := r.deprecatedMobileKeys[permanent]
	assert.True(t, expiringDeprecated, "expiring mobile key should be queued for expiry")
	assert.True(t, expiringAccepted, "expiring mobile key is still accepted until it expires")
	assert.False(t, permanentDeprecated)

	// PrimaryCredentials keeps the permanent key but excludes the deprecated (expiring) one.
	assert.Contains(t, r.PrimaryCredentials(), SDKCredential(permanent))
	assert.NotContains(t, r.PrimaryCredentials(), SDKCredential(expiring))
}

func TestReconcileExpiringSDKKey(t *testing.T) {
	r := newTestRotator()
	old := config.SDKKey("old")
	anchor := config.SDKKey("anchor")
	start := time.Unix(1000, 0)

	// Start anchored on the old key.
	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(old).WithAnchorSDKKey(old)), start))
	r.StepTime(start)

	// Re-anchor onto anchor, keeping old valid (deprecated) for an hour.
	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithAnchorSDKKey(anchor).WithExpiringSDKKey(old, start.Add(time.Hour))),
		start))
	additions, expirations := r.StepTime(start)

	assert.ElementsMatch(t, []SDKCredential{anchor}, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, anchor, r.SDKKey())
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
	assert.ElementsMatch(t, []SDKCredential{old}, r.DeprecatedCredentials())

	// Just before expiry: old still deprecated.
	_, expirations = r.StepTime(start.Add(time.Hour))
	assert.Empty(t, expirations)

	// Just after expiry: old is dropped entirely.
	_, expirations = r.StepTime(start.Add(time.Hour + time.Millisecond))
	assert.ElementsMatch(t, []SDKCredential{old}, expirations)
	assert.Empty(t, r.DeprecatedCredentials())
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}
