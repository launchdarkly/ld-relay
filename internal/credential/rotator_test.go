package credential

import (
	"fmt"
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

func TestImmediateKeyExpiration(t *testing.T) {
	kinds := []struct {
		name   string
		keys   []SDKCredential
		getKey func(*Rotator) SDKCredential
	}{
		{
			name:   "sdk keys",
			keys:   []SDKCredential{config.SDKKey("key1"), config.SDKKey("key2"), config.SDKKey("key3")},
			getKey: func(r *Rotator) SDKCredential { return r.SDKKey() },
		},
		{
			name:   "mobile keys",
			keys:   []SDKCredential{config.MobileKey("key1"), config.MobileKey("key2"), config.MobileKey("key3")},
			getKey: func(r *Rotator) SDKCredential { return r.MobileKey() },
		},
		{
			name:   "environment IDs",
			keys:   []SDKCredential{config.EnvironmentID("id1"), config.EnvironmentID("id2"), config.EnvironmentID("id3")},
			getKey: func(r *Rotator) SDKCredential { return r.EnvironmentID() },
		},
	}

	for _, c := range kinds {
		t.Run(c.name, func(t *testing.T) {
			mockLog := ldlogtest.NewMockLog()
			rotator := NewRotator(mockLog.Loggers)

			// The first rotation shouldn't trigger any expirations because there was no previous key.
			rotator.Rotate(c.keys[0])
			additions, _ := rotator.StepTime(time.Now())
			assert.ElementsMatch(t, c.keys[0:1], additions)
			assert.Equal(t, c.keys[0], c.getKey(rotator))

			// The second rotation should trigger a deprecation of key1.
			rotator.Rotate(c.keys[1])
			additions, expirations := rotator.StepTime(time.Now())
			assert.ElementsMatch(t, c.keys[1:2], additions)
			assert.ElementsMatch(t, c.keys[0:1], expirations)
			assert.Equal(t, c.keys[1], c.getKey(rotator))

			// The third rotation should trigger a deprecation of key2.
			rotator.Rotate(c.keys[2])
			additions, expirations = rotator.StepTime(time.Now())
			assert.ElementsMatch(t, c.keys[2:3], additions)
			assert.ElementsMatch(t, c.keys[1:2], expirations)
			assert.Equal(t, c.keys[2], c.getKey(rotator))
		})
	}
}

func TestManyImmediateKeyExpirations(t *testing.T) {

	kinds := []struct {
		name    string
		makeKey func(string) SDKCredential
		getKey  func(*Rotator) SDKCredential
	}{
		{
			name:    "sdk keys",
			makeKey: func(s string) SDKCredential { return config.SDKKey(s) },
			getKey:  func(r *Rotator) SDKCredential { return r.SDKKey() },
		},
		{
			name:    "mobile keys",
			makeKey: func(s string) SDKCredential { return config.MobileKey(s) },
			getKey:  func(r *Rotator) SDKCredential { return r.MobileKey() },
		},
		{
			name:    "environment IDs",
			makeKey: func(s string) SDKCredential { return config.EnvironmentID(s) },
			getKey:  func(r *Rotator) SDKCredential { return r.EnvironmentID() },
		},
	}

	for _, c := range kinds {
		t.Run(c.name, func(t *testing.T) {
			mockLog := ldlogtest.NewMockLog()
			rotator := NewRotator(mockLog.Loggers)

			const numKeys = 100
			for i := 0; i < numKeys; i++ {
				key := c.makeKey(fmt.Sprintf("key%v", i))
				rotator.Rotate(key)
			}

			assert.Equal(t, c.makeKey(fmt.Sprintf("key%v", numKeys-1)), c.getKey(rotator))

			additions, expirations := rotator.StepTime(time.Now())
			assert.Len(t, additions, numKeys)
			assert.Len(t, expirations, numKeys-1) // because the last key is still active
		})
	}
}

func TestImmediateSDKKeyDeprecationEvenIfGracePeriodIsPresent(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	key0 := config.SDKKey("key0")
	key1 := config.SDKKey("key1")
	key2 := config.SDKKey("key2")

	rotator.Initialize([]SDKCredential{key0})

	start := time.Unix(1000, 0)
	halftime := start.Add(30 * time.Minute)
	expiry := start.Add(1 * time.Hour)

	rotator.RotateWithGrace(key1, NewGracePeriod(key0, expiry, start))

	additions, expirations := rotator.StepTime(halftime)
	assert.ElementsMatch(t, []SDKCredential{key1}, additions)
	assert.Empty(t, expirations)

	// The deprecated key0 given here can be thought of as "stale" or otherwise already-seen by the rotator.
	// In this case, it should be effectively ignored but the new key2 should still trigger rotation of the previous
	// primary key.
	rotator.RotateWithGrace(key2, NewGracePeriod(key0, expiry, halftime))

	additions, expirations = rotator.StepTime(halftime)
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)

	additions, expirations = rotator.StepTime(expiry.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{key0}, expirations)
}

func TestSDKKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	const (
		key1 = config.SDKKey("key1")
		key2 = config.SDKKey("key2")
	)

	start := time.Unix(10000, 0)

	halfTime := start.Add(30 * time.Second)
	deprecationTime := start.Add(1 * time.Minute)

	rotator.Initialize([]SDKCredential{key1})

	rotator.RotateWithGrace(key2, NewGracePeriod(key1, deprecationTime, halfTime))
	additions, expirations := rotator.StepTime(halfTime)
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.Empty(t, expirations)

	additions, expirations = rotator.StepTime(deprecationTime)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	additions, expirations = rotator.StepTime(deprecationTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
}

func TestManyConcurrentSDKKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	makeKey := func(i int) config.SDKKey {
		return config.SDKKey(fmt.Sprintf("key%v", i))
	}

	rotator.Initialize([]SDKCredential{config.SDKKey("key0")})

	const numKeys = 250
	now := time.Unix(10000, 0)
	expiryTime := now.Add(1 * time.Hour)

	var keysDeprecated []SDKCredential
	var keysAdded []SDKCredential

	for i := 0; i < numKeys; i++ {
		previousKey := makeKey(i)
		nextKey := makeKey(i + 1)

		keysDeprecated = append(keysDeprecated, previousKey)
		keysAdded = append(keysAdded, nextKey)

		rotator.RotateWithGrace(nextKey, NewGracePeriod(previousKey, expiryTime, now))
	}

	// The last key added should be the current primary key.
	assert.Equal(t, keysAdded[len(keysAdded)-1], rotator.SDKKey())

	// Until and including the exact expiry timestamp, there should be no expirations.
	additions, expirations := rotator.StepTime(expiryTime)
	assert.ElementsMatch(t, keysAdded, additions)
	assert.Empty(t, expirations)

	// One moment after the expiry time, we should now have a batch of expirations.
	additions, expirations = rotator.StepTime(expiryTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, keysDeprecated, expirations)
}

func TestSDKKeyExpiredInThePastIsNotAdded(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	primaryKey := config.SDKKey("primary")
	obsoleteKey := config.SDKKey("obsolete")
	obsoleteExpiry := time.Unix(1000000, 0)
	now := obsoleteExpiry.Add(1 * time.Hour)

	rotator.RotateWithGrace(primaryKey, NewGracePeriod(obsoleteKey, obsoleteExpiry, now))

	additions, expirations := rotator.StepTime(now)
	assert.ElementsMatch(t, []SDKCredential{primaryKey}, additions)
	assert.Empty(t, expirations)
}

func TestSDKKeyDeprecationWithAlreadyExpiredGraceRevokesPreviousPrimary(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	key1 := config.SDKKey("key1")
	key2 := config.SDKKey("key2")

	expiry := time.Unix(10000, 0)
	now := expiry.Add(1 * time.Hour) // now is after the grace period's expiry

	rotator.Initialize([]SDKCredential{key1})

	// Rotate key1 -> key2, but the deprecation grace for the outgoing key1 has already elapsed.
	// key2 becomes primary; key1 must be revoked immediately rather than lingering forever as an
	// accepted-but-untracked key. (This mirrors the equivalent mobile-key behavior.)
	rotator.RotateWithGrace(key2, NewGracePeriod(key1, expiry, now))

	assert.Equal(t, key2, rotator.SDKKey())

	additions, expirations := rotator.StepTime(now)
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
	assert.Empty(t, rotator.DeprecatedCredentials())
}

func TestReAnchoringDeprecatedSDKKeyRemovesItFromDeprecatedSet(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	key1 := config.SDKKey("key1")
	key2 := config.SDKKey("key2")

	start := time.Unix(10000, 0)
	expiry := start.Add(1 * time.Hour)

	rotator.Initialize([]SDKCredential{key1})

	// Rotate key1 -> key2 with grace; key1 enters the deprecated set.
	rotator.RotateWithGrace(key2, NewGracePeriod(key1, expiry, start))
	rotator.StepTime(start)
	assert.ElementsMatch(t, []SDKCredential{key1}, rotator.DeprecatedCredentials())

	// Re-anchor key2 -> key1 before key1's grace expires. key1 must be promoted out of the
	// deprecated set; otherwise the cleanup ticker would later expire the active primary.
	rotator.Rotate(key1)
	assert.Equal(t, key1, rotator.SDKKey())
	assert.Empty(t, rotator.DeprecatedCredentials())

	additions, expirations := rotator.StepTime(start)
	assert.ElementsMatch(t, []SDKCredential{key1}, additions)
	assert.ElementsMatch(t, []SDKCredential{key2}, expirations)

	// Well past the original grace expiry, key1 (the active primary) must NOT be expired.
	additions, expirations = rotator.StepTime(expiry.Add(1 * time.Hour))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, key1, rotator.SDKKey())
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

func TestRotateWithGraceMobileKey(t *testing.T) {
	t.Run("does not panic with non-nil grace period", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		mob1 := config.MobileKey("mob1")
		mob2 := config.MobileKey("mob2")

		start := time.Unix(10000, 0)
		expiry := start.Add(1 * time.Hour)

		rotator.Initialize([]SDKCredential{mob1})

		// GracePeriod.key is SDK-key typed; pass a zero value since mobile-key rotation
		// does not use that identifier field.
		assert.NotPanics(t, func() {
			rotator.RotateWithGrace(mob2, NewGracePeriod(config.SDKKey(""), expiry, start))
		})

		assert.Equal(t, mob2, rotator.MobileKey())

		// mob2 is a new addition; mob1 is in the deprecated set (not yet expired),
		// so it should not appear as an expiration here.
		additions, expirations := rotator.StepTime(start)
		assert.ElementsMatch(t, []SDKCredential{mob2}, additions)
		assert.Empty(t, expirations)

		// One moment past the grace period, the cleanup ticker expires mob1 and evicts it from the
		// accepted set entirely.
		additions, expirations = rotator.StepTime(expiry.Add(1 * time.Millisecond))
		assert.Empty(t, additions)
		assert.ElementsMatch(t, []SDKCredential{mob1}, expirations)
		assert.NotContains(t, rotator.PrimaryCredentials(), SDKCredential(mob1))
		assert.NotContains(t, rotator.DeprecatedCredentials(), SDKCredential(mob1))
	})

	t.Run("immediately revokes outgoing key when grace period is already expired", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		mob1 := config.MobileKey("mob1")
		mob2 := config.MobileKey("mob2")

		expiry := time.Unix(10000, 0)
		now := expiry.Add(1 * time.Hour) // now is after expiry

		rotator.Initialize([]SDKCredential{mob1})

		rotator.RotateWithGrace(mob2, NewGracePeriod(config.SDKKey(""), expiry, now))

		assert.Equal(t, mob2, rotator.MobileKey())

		additions, expirations := rotator.StepTime(now)
		assert.ElementsMatch(t, []SDKCredential{mob2}, additions)
		assert.ElementsMatch(t, []SDKCredential{mob1}, expirations)
		// The immediately-revoked key must leave the accepted set, not linger in PrimaryCredentials.
		assert.NotContains(t, rotator.PrimaryCredentials(), SDKCredential(mob1))
	})

	t.Run("immediately revokes outgoing key when grace is nil", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		mob1 := config.MobileKey("mob1")
		mob2 := config.MobileKey("mob2")

		rotator.Initialize([]SDKCredential{mob1})
		rotator.RotateWithGrace(mob2, nil)

		assert.Equal(t, mob2, rotator.MobileKey())

		additions, expirations := rotator.StepTime(time.Now())
		assert.ElementsMatch(t, []SDKCredential{mob2}, additions)
		assert.ElementsMatch(t, []SDKCredential{mob1}, expirations)
		// The immediately-revoked key must leave the accepted set, not linger in PrimaryCredentials.
		assert.NotContains(t, rotator.PrimaryCredentials(), SDKCredential(mob1))
	})

	t.Run("re-promoting a deprecated key removes it from the deprecated set", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		mob1 := config.MobileKey("mob1")
		mob2 := config.MobileKey("mob2")

		start := time.Unix(10000, 0)
		expiry := start.Add(1 * time.Hour)

		rotator.Initialize([]SDKCredential{mob1})

		// Rotate mob1 → mob2 with grace; mob1 enters deprecatedMobileKeys.
		rotator.RotateWithGrace(mob2, NewGracePeriod(config.SDKKey(""), expiry, start))
		rotator.StepTime(start)

		// Rotate back mob2 → mob1; mob1 should be promoted out of the deprecated set.
		rotator.RotateWithGrace(mob1, nil)

		assert.Equal(t, mob1, rotator.MobileKey())

		// mob1 should appear only as an addition, not also as an expiration.
		additions, expirations := rotator.StepTime(start)
		assert.ElementsMatch(t, []SDKCredential{mob1}, additions)
		assert.ElementsMatch(t, []SDKCredential{mob2}, expirations)
	})
}

func TestRotateSDKKeyRePromoteClearsDeprecation(t *testing.T) {
	// Re-promoting a deprecated SDK key back to primary must clear its deprecated mark, so
	// PrimaryCredentials lists it (mirrors the mobile re-promote behavior).
	rotator := newTestRotator()
	key1 := config.SDKKey("key1")
	key2 := config.SDKKey("key2")
	start := time.Unix(10000, 0)

	rotator.Initialize([]SDKCredential{key1})
	rotator.RotateWithGrace(key2, NewGracePeriod(key1, start.Add(time.Hour), start)) // deprecate key1
	rotator.StepTime(start)
	assert.ElementsMatch(t, []SDKCredential{key1}, rotator.DeprecatedCredentials())

	rotator.RotateWithGrace(key1, nil) // re-promote key1
	rotator.StepTime(start)

	assert.Equal(t, key1, rotator.SDKKey())
	assert.Contains(t, rotator.PrimaryCredentials(), SDKCredential(key1))
	assert.NotContains(t, rotator.DeprecatedCredentials(), SDKCredential(key1))
}

func TestRotateSDKKeyWithExpiredGraceRevokesPrevious(t *testing.T) {
	// A legacy SDK rotation whose grace period is already expired must revoke the swapped-out key,
	// not leave it enabled alongside the new anchor (mirrors updateMobileKey).
	rotator := newTestRotator()
	key1 := config.SDKKey("key1")
	key2 := config.SDKKey("key2")
	expiry := time.Unix(10000, 0)
	now := expiry.Add(time.Hour) // now is after expiry

	rotator.Initialize([]SDKCredential{key1})
	rotator.RotateWithGrace(key2, NewGracePeriod(key1, expiry, now))

	assert.Equal(t, key2, rotator.SDKKey())
	additions, expirations := rotator.StepTime(now)
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
	assert.NotContains(t, rotator.PrimaryCredentials(), SDKCredential(key1))
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

func TestReconcileClearsStaleDeprecationForAcceptedKey(t *testing.T) {
	// A key left in the deprecated set by the legacy rotation path must be treated as fully accepted
	// once a reconcile includes it, not silently skipped by PrimaryCredentials.
	r := newTestRotator()
	old := config.SDKKey("old")
	anchor := config.SDKKey("anchor")
	now := time.Unix(1000, 0)

	r.Initialize([]SDKCredential{old})
	r.RotateWithGrace(anchor, NewGracePeriod(old, now.Add(time.Hour), now)) // deprecate `old` with grace
	r.StepTime(now)
	require.ElementsMatch(t, []SDKCredential{old}, r.DeprecatedCredentials())

	// Reconcile to a set that fully accepts both keys.
	r.Reconcile(
		mustBuild(t, NewAcceptedSetBuilder().WithPrimarySDKKey(anchor).WithSDKKey(old)), now)
	r.StepTime(now)

	assert.Contains(t, r.PrimaryCredentials(), SDKCredential(old))
	assert.Empty(t, r.DeprecatedCredentials())
}

func TestReconcileExpiringKeysAreEvictedByStepTime(t *testing.T) {
	// End-to-end on the reconcile path: a reconcile records per-key expiry as data on the accepted
	// entry, and the cleanup ticker (StepTime) later drops both the expiring SDK key and the expiring
	// mobile key once their expiry elapses — without ever passing through the legacy deprecated maps.
	// The anchor and primary mobile key carry no expiry and survive.
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

func TestMobileKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	const (
		mob1 = config.MobileKey("mob1")
		mob2 = config.MobileKey("mob2")
	)

	start := time.Unix(10000, 0)
	halfTime := start.Add(30 * time.Second)
	deprecationTime := start.Add(1 * time.Minute)

	rotator.Initialize([]SDKCredential{mob1})

	rotator.RotateWithGrace(mob2, NewGracePeriod(config.SDKKey(""), deprecationTime, halfTime))
	additions, expirations := rotator.StepTime(halfTime)
	assert.ElementsMatch(t, []SDKCredential{mob2}, additions)
	assert.Empty(t, expirations)

	// At the exact expiry, not yet expired.
	additions, expirations = rotator.StepTime(deprecationTime)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	// One moment past the expiry: mob1 is expired.
	additions, expirations = rotator.StepTime(deprecationTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{mob1}, expirations)
}

func TestManyConcurrentMobileKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	makeKey := func(i int) config.MobileKey {
		return config.MobileKey(fmt.Sprintf("mob%v", i))
	}

	rotator.Initialize([]SDKCredential{makeKey(0)})

	const numKeys = 50
	now := time.Unix(10000, 0)
	expiryTime := now.Add(1 * time.Hour)

	var keysDeprecated []SDKCredential
	var keysAdded []SDKCredential

	for i := 0; i < numKeys; i++ {
		nextKey := makeKey(i + 1)
		keysDeprecated = append(keysDeprecated, makeKey(i))
		keysAdded = append(keysAdded, nextKey)
		rotator.RotateWithGrace(nextKey, NewGracePeriod(config.SDKKey(""), expiryTime, now))
	}

	assert.Equal(t, keysAdded[len(keysAdded)-1], rotator.MobileKey())

	// Until and including the exact expiry timestamp, no expirations.
	additions, expirations := rotator.StepTime(expiryTime)
	assert.ElementsMatch(t, keysAdded, additions)
	assert.Empty(t, expirations)

	// One moment after the expiry time: batch of expirations.
	additions, expirations = rotator.StepTime(expiryTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, keysDeprecated, expirations)
}

func TestMixedSDKAndMobileKeyExpiry(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	sdk1 := config.SDKKey("sdk1")
	sdk2 := config.SDKKey("sdk2")
	mob1 := config.MobileKey("mob1")
	mob2 := config.MobileKey("mob2")

	now := time.Unix(10000, 0)
	expiry := now.Add(1 * time.Hour)

	rotator.Initialize([]SDKCredential{sdk1, mob1})

	rotator.RotateWithGrace(sdk2, NewGracePeriod(sdk1, expiry, now))
	rotator.StepTime(now)

	rotator.RotateWithGrace(mob2, NewGracePeriod(config.SDKKey(""), expiry, now))
	rotator.StepTime(now)

	// Both sdk1 and mob1 should expire at the same tick.
	additions, expirations := rotator.StepTime(expiry.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{sdk1, mob1}, expirations)
}

func TestDeprecatedCredentialsIncludesMobileKeys(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	sdk1 := config.SDKKey("sdk1")
	sdk2 := config.SDKKey("sdk2")
	mob1 := config.MobileKey("mob1")
	mob2 := config.MobileKey("mob2")

	now := time.Unix(10000, 0)
	expiry := now.Add(1 * time.Hour)

	rotator.Initialize([]SDKCredential{sdk1, mob1})

	rotator.RotateWithGrace(sdk2, NewGracePeriod(sdk1, expiry, now))
	rotator.StepTime(now)

	rotator.RotateWithGrace(mob2, NewGracePeriod(config.SDKKey(""), expiry, now))
	rotator.StepTime(now)

	deprecated := rotator.DeprecatedCredentials()
	assert.ElementsMatch(t, []SDKCredential{sdk1, mob1}, deprecated)
}

// reconcileSet is a test helper that builds an AcceptedSet and calls Reconcile on r.
func reconcileSet(t *testing.T, r *Rotator, build func(*AcceptedSetBuilder)) {
	t.Helper()
	b := NewAcceptedSetBuilder()
	build(b)
	set, err := b.Build()
	require.NoError(t, err)
	r.Reconcile(set, time.Unix(0, 0))
}

// TestNonAnchorSDKKeyEntries verifies that NonAnchorSDKKeyEntries returns all accepted non-anchor SDK
// keys sorted by identifier, with anchor excluded and expiry populated from both reconcile and legacy paths.
func TestNonAnchorSDKKeyEntries(t *testing.T) {
	t.Run("single anchor returns empty slice", func(t *testing.T) {
		r := newTestRotator()
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithSDKKeyIdentifier("sdk-anchor", "default").
				WithPrimaryMobileKey("mob-primary")
		})
		entries := r.NonAnchorSDKKeyEntries()
		assert.Empty(t, entries)
	})

	t.Run("multiple keys sorted by identifier", func(t *testing.T) {
		r := newTestRotator()
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithSDKKeyIdentifier("sdk-anchor", "default").
				WithSDKKey("sdk-b").
				WithSDKKeyIdentifier("sdk-b", "b-service").
				WithSDKKey("sdk-a").
				WithSDKKeyIdentifier("sdk-a", "a-service").
				WithPrimaryMobileKey("mob-primary")
		})
		entries := r.NonAnchorSDKKeyEntries()
		require.Len(t, entries, 2)
		// Sorted alphabetically by identifier: a-service < b-service.
		assert.Equal(t, config.SDKKey("sdk-a"), entries[0].Value)
		assert.Equal(t, "a-service", entries[0].Identifier)
		assert.Nil(t, entries[0].Expiry)
		assert.Equal(t, config.SDKKey("sdk-b"), entries[1].Value)
		assert.Equal(t, "b-service", entries[1].Identifier)
	})

	t.Run("expiring key expiry populated", func(t *testing.T) {
		r := newTestRotator()
		expiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithSDKKeyIdentifier("sdk-anchor", "default").
				WithExpiringSDKKey("sdk-old", expiry).
				WithSDKKeyIdentifier("sdk-old", "old-key").
				WithPrimaryMobileKey("mob-primary")
		})
		entries := r.NonAnchorSDKKeyEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, config.SDKKey("sdk-old"), entries[0].Value)
		assert.Equal(t, "old-key", entries[0].Identifier)
		require.NotNil(t, entries[0].Expiry)
		assert.Equal(t, expiry, *entries[0].Expiry)
	})

	t.Run("legacy RotateWithGrace key included with expiry", func(t *testing.T) {
		r := newTestRotator()
		now := time.Unix(1000, 0)
		expiry := now.Add(time.Hour)
		r.Initialize([]SDKCredential{config.SDKKey("sdk-old")})
		r.RotateWithGrace(config.SDKKey("sdk-new"), NewGracePeriod("sdk-old", expiry, now))
		r.StepTime(now)

		entries := r.NonAnchorSDKKeyEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, config.SDKKey("sdk-old"), entries[0].Value)
		assert.Equal(t, "", entries[0].Identifier) // legacy path has no identifier
		require.NotNil(t, entries[0].Expiry)
		assert.Equal(t, expiry, *entries[0].Expiry)
	})

	t.Run("no-identifier keys sort after identified keys", func(t *testing.T) {
		r := newTestRotator()
		now := time.Unix(1000, 0)
		expiry := now.Add(time.Hour)
		// sdk-legacy has no identifier (legacy path); sdk-named has one (reconcile path).
		r.Initialize([]SDKCredential{config.SDKKey("sdk-old")})
		r.RotateWithGrace(config.SDKKey("sdk-anchor"), NewGracePeriod("sdk-old", expiry, now))
		r.StepTime(now)
		// Now add a reconcile key with an identifier.
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithSDKKeyIdentifier("sdk-anchor", "default").
				WithSDKKey("sdk-named").
				WithSDKKeyIdentifier("sdk-named", "named").
				WithSDKKey("sdk-old") // keep legacy key in set so it isn't revoked
		})

		entries := r.NonAnchorSDKKeyEntries()
		require.Len(t, entries, 2)
		// "named" has an identifier; sdk-old has none — identified entries sort first.
		assert.Equal(t, "named", entries[0].Identifier)
		assert.Equal(t, "", entries[1].Identifier)
	})
}

// TestNonPrimaryMobileKeyEntries verifies NonPrimaryMobileKeyEntries returns all non-primary mobile keys.
func TestNonPrimaryMobileKeyEntries(t *testing.T) {
	t.Run("single primary mobile key returns empty slice", func(t *testing.T) {
		r := newTestRotator()
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithPrimaryMobileKey("mob-primary").
				WithMobileKeyIdentifier("mob-primary", "mob-1")
		})
		entries := r.NonPrimaryMobileKeyEntries()
		assert.Empty(t, entries)
	})

	t.Run("multiple mobile keys sorted by identifier", func(t *testing.T) {
		r := newTestRotator()
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithMobileKey("mob-primary").
				WithMobileKeyIdentifier("mob-primary", "mob-1").
				WithMobileKey("mob-secondary").
				WithMobileKeyIdentifier("mob-secondary", "mob-2").
				WithMobileKey("mob-third").
				WithMobileKeyIdentifier("mob-third", "mob-0").
				WithPrimaryMobileKey("mob-primary")
		})
		entries := r.NonPrimaryMobileKeyEntries()
		require.Len(t, entries, 2)
		// mob-0 < mob-2 alphabetically.
		assert.Equal(t, config.MobileKey("mob-third"), entries[0].Value)
		assert.Equal(t, "mob-0", entries[0].Identifier)
		assert.Equal(t, config.MobileKey("mob-secondary"), entries[1].Value)
		assert.Equal(t, "mob-2", entries[1].Identifier)
	})

	t.Run("expiring mobile key expiry populated", func(t *testing.T) {
		r := newTestRotator()
		expiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithMobileKey("mob-primary").
				WithMobileKeyIdentifier("mob-primary", "mob-1").
				WithExpiringMobileKey("mob-old", expiry).
				WithMobileKeyIdentifier("mob-old", "mob-old").
				WithPrimaryMobileKey("mob-primary")
		})
		entries := r.NonPrimaryMobileKeyEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, config.MobileKey("mob-old"), entries[0].Value)
		require.NotNil(t, entries[0].Expiry)
		assert.Equal(t, expiry, *entries[0].Expiry)
	})
}

// TestEarliestExpiringNonAnchorSDKKey verifies the deterministic expiring-key selection used by the
// status endpoint's legacy expiringSdkKey field.
func TestEarliestExpiringNonAnchorSDKKey(t *testing.T) {
	t.Run("no keys returns false", func(t *testing.T) {
		r := newTestRotator()
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").WithPrimaryMobileKey("mob-primary")
		})
		_, ok := r.EarliestExpiringNonAnchorSDKKey()
		assert.False(t, ok)
	})

	t.Run("only permanent non-anchor keys returns false", func(t *testing.T) {
		r := newTestRotator()
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithSDKKey("sdk-perm").
				WithPrimaryMobileKey("mob-primary")
		})
		_, ok := r.EarliestExpiringNonAnchorSDKKey()
		assert.False(t, ok)
	})

	t.Run("single expiring key (reconcile path)", func(t *testing.T) {
		r := newTestRotator()
		expiry := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithExpiringSDKKey("sdk-old", expiry).
				WithPrimaryMobileKey("mob-primary")
		})
		key, ok := r.EarliestExpiringNonAnchorSDKKey()
		assert.True(t, ok)
		assert.Equal(t, config.SDKKey("sdk-old"), key)
	})

	t.Run("picks soonest among multiple expiring keys", func(t *testing.T) {
		r := newTestRotator()
		sooner := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		later := time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)
		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithExpiringSDKKey("sdk-sooner", sooner).
				WithExpiringSDKKey("sdk-later", later).
				WithPrimaryMobileKey("mob-primary")
		})
		key, ok := r.EarliestExpiringNonAnchorSDKKey()
		assert.True(t, ok)
		assert.Equal(t, config.SDKKey("sdk-sooner"), key)
	})

	t.Run("legacy RotateWithGrace key is found", func(t *testing.T) {
		r := newTestRotator()
		now := time.Unix(1000, 0)
		expiry := now.Add(time.Hour)
		r.Initialize([]SDKCredential{config.SDKKey("sdk-old")})
		r.RotateWithGrace(config.SDKKey("sdk-new"), NewGracePeriod("sdk-old", expiry, now))
		r.StepTime(now)

		key, ok := r.EarliestExpiringNonAnchorSDKKey()
		assert.True(t, ok)
		assert.Equal(t, config.SDKKey("sdk-old"), key)
	})

	t.Run("picks soonest across both reconcile and legacy paths", func(t *testing.T) {
		r := newTestRotator()
		now := time.Unix(1000, 0)
		legacyExpiry := now.Add(2 * time.Hour) // later
		reconcileExpiry := now.Add(30 * time.Minute) // sooner

		r.Initialize([]SDKCredential{config.SDKKey("sdk-legacy")})
		r.RotateWithGrace(config.SDKKey("sdk-anchor"), NewGracePeriod("sdk-legacy", legacyExpiry, now))
		r.StepTime(now)

		reconcileSet(t, r, func(b *AcceptedSetBuilder) {
			b.WithPrimarySDKKey("sdk-anchor").
				WithExpiringSDKKey("sdk-reconcile", reconcileExpiry).
				WithSDKKey("sdk-legacy") // include legacy key so it isn't revoked
		})

		key, ok := r.EarliestExpiringNonAnchorSDKKey()
		assert.True(t, ok)
		assert.Equal(t, config.SDKKey("sdk-reconcile"), key)
	})
}
