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

func TestAcceptedSetBuilderRequiresSDKKey(t *testing.T) {
	// A set with no SDK key is a caller error, not a silent no-op.
	_, err := NewAcceptedSetBuilder().
		WithMobileKey(config.MobileKey("mob")).
		WithEnvironmentID(config.EnvironmentID("env")).
		Build()
	require.ErrorIs(t, err, errAcceptedSetMissingSDKKey)

	// With at least one SDK key, Build succeeds.
	set, err := NewAcceptedSetBuilder().WithSDKKey(config.SDKKey("sdk")).Build()
	require.NoError(t, err)
	assert.True(t, set.hasSDKKey(config.SDKKey("sdk")))
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

	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor)), anchor, now))
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
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithSDKKey(other)), anchor, now))
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
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithMobileKey(mob1).WithMobileKey(mob2)), anchor, now))
	additions, _ := r.StepTime(now)

	// Every mobile key is accepted; the first is the primary.
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, additions)
	assert.Equal(t, mob1, r.MobileKey())
	assert.ElementsMatch(t, []SDKCredential{anchor, mob1, mob2}, r.PrimaryCredentials())
}

func TestReconcileMalformedAnchorPreservesState(t *testing.T) {
	r := newTestRotator()
	anchor := config.SDKKey("anchor")
	now := time.Now()

	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor)), anchor, now))
	r.StepTime(now)

	// The anchor is not among the set's SDK keys: malformed, no mutation.
	bad := mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(config.SDKKey("other")))
	var malformed *MalformedCredentialSetError
	require.ErrorAs(t, r.Reconcile(bad, config.SDKKey("not-in-set"), now), &malformed)

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
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithSDKKey(other).WithMobileKey(mob)), anchor, now))
	r.StepTime(now)

	// Reconciling to just the anchor revokes the omitted server and mobile keys.
	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor)), anchor, now))
	additions, expirations := r.StepTime(now)

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{other, mob}, expirations)
	assert.ElementsMatch(t, []SDKCredential{anchor}, r.PrimaryCredentials())
}

func TestReconcileExpiringSDKKey(t *testing.T) {
	r := newTestRotator()
	old := config.SDKKey("old")
	anchor := config.SDKKey("anchor")
	start := time.Unix(1000, 0)

	// Start anchored on the old key.
	require.NoError(t, r.Reconcile(mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(old)), old, start))
	r.StepTime(start)

	// Re-anchor onto anchor, keeping old valid (deprecated) for an hour.
	require.NoError(t, r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithSDKKey(anchor).WithExpiringSDKKey(old, start.Add(time.Hour))),
		anchor, start))
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
