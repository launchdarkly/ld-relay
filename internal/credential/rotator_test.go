package credential

import (
	"fmt"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
)

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

func TestSetAdditionalSDKKeysAddsActiveKeys(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})
	_, _ = rotator.StepTime(time.Now())

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"extra1", "extra2"}, nil)
	additions, expirations := rotator.StepTime(time.Now())

	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("extra1"), config.SDKKey("extra2")}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.SDKKey{"primary", "extra1", "extra2"}, rotator.ActiveSDKKeys())
}

func TestSetAdditionalSDKKeysAddsExpiringKeys(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})
	_, _ = rotator.StepTime(time.Now())

	expiry := time.Unix(2000, 0)
	rotator.SetAdditionalSDKKeys(nil, map[config.SDKKey]time.Time{
		"expiring1": expiry,
	})
	additions, expirations := rotator.StepTime(time.Unix(1000, 0))

	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("expiring1")}, additions)
	assert.Empty(t, expirations)
	// Expiring keys are not "active" in the ActiveSDKKeys sense.
	assert.ElementsMatch(t, []config.SDKKey{"primary"}, rotator.ActiveSDKKeys())
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("expiring1")}, rotator.DeprecatedCredentials())
}

func TestSetAdditionalSDKKeysOmissionRevokesImmediately(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"extra1", "extra2"}, nil)
	_, _ = rotator.StepTime(time.Now())

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"extra1"}, nil)
	additions, expirations := rotator.StepTime(time.Now())

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("extra2")}, expirations)
	assert.ElementsMatch(t, []config.SDKKey{"primary", "extra1"}, rotator.ActiveSDKKeys())
}

func TestSetAdditionalSDKKeysActiveToExpiringStaysMapped(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"k"}, nil)
	additions, expirations := rotator.StepTime(time.Now())
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("k")}, additions)
	assert.Empty(t, expirations)

	// Now mark the same key as expiring; it should stay mapped (no churn in additions/expirations).
	expiry := time.Unix(5000, 0)
	rotator.SetAdditionalSDKKeys(nil, map[config.SDKKey]time.Time{"k": expiry})
	additions, expirations = rotator.StepTime(time.Unix(1000, 0))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.SDKKey{"primary"}, rotator.ActiveSDKKeys())
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("k")}, rotator.DeprecatedCredentials())
}

func TestSetAdditionalSDKKeysExpiringToActiveStaysMapped(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	expiry := time.Unix(5000, 0)
	rotator.SetAdditionalSDKKeys(nil, map[config.SDKKey]time.Time{"k": expiry})
	additions, expirations := rotator.StepTime(time.Unix(1000, 0))
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("k")}, additions)
	assert.Empty(t, expirations)

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"k"}, nil)
	additions, expirations = rotator.StepTime(time.Unix(2000, 0))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.SDKKey{"primary", "k"}, rotator.ActiveSDKKeys())
}

func TestSetAdditionalSDKKeysAcceptsUpdatedExpiry(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	earlyExpiry := time.Unix(2000, 0)
	rotator.SetAdditionalSDKKeys(nil, map[config.SDKKey]time.Time{"k": earlyExpiry})
	additions, _ := rotator.StepTime(time.Unix(1000, 0))
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("k")}, additions)

	// Extend the expiry.
	lateExpiry := time.Unix(10000, 0)
	rotator.SetAdditionalSDKKeys(nil, map[config.SDKKey]time.Time{"k": lateExpiry})

	// At a time after the original expiry but before the new one, the key should still be alive.
	additions, expirations := rotator.StepTime(time.Unix(5000, 0))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	// After the new expiry, the key should be revoked via StepTime.
	additions, expirations = rotator.StepTime(time.Unix(11000, 0))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("k")}, expirations)
}

func TestSetAdditionalSDKKeysIdempotent(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"k1", "k2"}, nil)
	_, _ = rotator.StepTime(time.Now())

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"k1", "k2"}, nil)
	additions, expirations := rotator.StepTime(time.Now())
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
}

func TestSetAdditionalSDKKeysFiltersPrimary(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	expiry := time.Unix(5000, 0)
	rotator.SetAdditionalSDKKeys(
		[]config.SDKKey{"primary", "extra"},
		map[config.SDKKey]time.Time{"primary": expiry},
	)
	additions, expirations := rotator.StepTime(time.Unix(1000, 0))

	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("extra")}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.SDKKey{"primary", "extra"}, rotator.ActiveSDKKeys())
}

func TestSetAdditionalSDKKeysFiltersRotationPredecessor(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	// Rotate to put "old" in the deprecatedSdkKeys map.
	rotator.Initialize([]SDKCredential{config.SDKKey("old")})
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("new"),
		NewGracePeriod(config.SDKKey("old"), start.Add(1*time.Hour), start),
	)
	_, _ = rotator.StepTime(start)

	// Now the platform mistakenly includes "old" in the additional list. The rotator should
	// ignore it -- the rotation flow is in charge of old's lifecycle.
	rotator.SetAdditionalSDKKeys([]config.SDKKey{"old", "extra"}, nil)

	// Only "extra" should have been queued; "old" stays in the deprecated map only.
	additions, expirations := rotator.StepTime(start)
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("extra")}, additions)
	assert.Empty(t, expirations)
	assert.NotContains(t, rotator.ActiveSDKKeys(), config.SDKKey("old"))
	assert.Contains(t, rotator.DeprecatedCredentials(), config.SDKKey("old"))

	// And the next patch omitting "old" from the additional list must NOT revoke it -- the
	// rotation grace timer still owns it.
	rotator.SetAdditionalSDKKeys([]config.SDKKey{"extra"}, nil)
	additions, expirations = rotator.StepTime(start.Add(1 * time.Minute))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.Contains(t, rotator.DeprecatedCredentials(), config.SDKKey("old"))

	// Confirm a warning was logged.
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "already in a rotation grace period")
}

func TestSetAdditionalSDKKeysWarnsOnPrimaryInList(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	rotator.SetAdditionalSDKKeys(
		[]config.SDKKey{"primary", "extra"},
		map[config.SDKKey]time.Time{"primary": time.Unix(5000, 0)},
	)

	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Primary SDK key .* appeared in additional-key list")
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Primary SDK key .* appeared in additional-key list with an expiry")
}

func TestSetAdditionalMobileKeysFiltersRotationPredecessor(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	rotator.Initialize([]SDKCredential{config.MobileKey("old")})
	start := time.Unix(10000, 0)
	rotator.RotateMobileKeyWithGrace(
		config.MobileKey("new"),
		NewMobileGracePeriod(config.MobileKey("old"), start.Add(1*time.Hour), start),
	)
	_, _ = rotator.StepTime(start)

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"old", "extra"}, nil)

	additions, expirations := rotator.StepTime(start)
	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("extra")}, additions)
	assert.Empty(t, expirations)
	assert.NotContains(t, rotator.ActiveMobileKeys(), config.MobileKey("old"))
	assert.Contains(t, rotator.DeprecatedCredentials(), config.MobileKey("old"))

	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "already in a rotation grace period")
}

func TestSetAdditionalSDKKeysPrefersExpiringWhenKeyAppearsInBoth(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	expiry := time.Unix(5000, 0)
	rotator.SetAdditionalSDKKeys(
		[]config.SDKKey{"shared"},
		map[config.SDKKey]time.Time{"shared": expiry},
	)
	additions, _ := rotator.StepTime(time.Unix(1000, 0))

	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("shared")}, additions)
	// Should be expiring, not active.
	assert.ElementsMatch(t, []config.SDKKey{"primary"}, rotator.ActiveSDKKeys())
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("shared")}, rotator.DeprecatedCredentials())
}

func TestSetAdditionalSDKKeysSurvivesPrimaryRotation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"k1", "k2"}, nil)
	additions, _ := rotator.StepTime(time.Now())
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("k1"), config.SDKKey("k2")}, additions)

	// Rotate the primary -- the additional set should stay intact.
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(config.SDKKey("primary-v2"), NewGracePeriod(config.SDKKey("primary"), start.Add(1*time.Hour), start))
	additions, expirations := rotator.StepTime(start)
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("primary-v2")}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.SDKKey{"primary-v2", "k1", "k2"}, rotator.ActiveSDKKeys())
}

func TestSetAdditionalSDKKeysDoesNotDisturbRotationPredecessor(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary-v1")})

	// Rotate primary, putting primary-v1 into the rotation grace map.
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("primary-v2"),
		NewGracePeriod(config.SDKKey("primary-v1"), start.Add(1*time.Hour), start),
	)
	_, _ = rotator.StepTime(start)

	// Now set an additional key. The rotation predecessor must NOT be revoked.
	rotator.SetAdditionalSDKKeys([]config.SDKKey{"extra"}, nil)
	additions, expirations := rotator.StepTime(start.Add(1 * time.Minute))
	assert.ElementsMatch(t, []SDKCredential{config.SDKKey("extra")}, additions)
	assert.Empty(t, expirations)
	assert.Contains(t, rotator.DeprecatedCredentials(), config.SDKKey("primary-v1"))
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

func TestPromotingAdditionalSDKKeyToPrimaryRemovesFromAdditionalSet(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"extra1", "extra2"}, nil)
	_, _ = rotator.StepTime(time.Now())

	// Promote "extra1" to primary. The old primary deprecates with a grace period.
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("extra1"),
		NewGracePeriod(config.SDKKey("primary"), start.Add(1*time.Hour), start),
	)

	// The new primary must report as the primary.
	assert.Equal(t, config.SDKKey("extra1"), rotator.SDKKey())

	// The promoted key is no longer in the additional set; extra2 still is.
	active := rotator.ActiveSDKKeys()
	assert.Contains(t, active, config.SDKKey("extra1"))
	assert.Contains(t, active, config.SDKKey("extra2"))
	assert.NotContains(t, active, config.SDKKey("primary"))

	// The old primary is in the deprecation map.
	assert.Contains(t, rotator.DeprecatedCredentials(), config.SDKKey("primary"))

	// Drain the additions queued by the rotation. The new primary (extra1) is queued so the env
	// context can start its upstream SDK client; downstream operations on it are idempotent.
	additions, _ := rotator.StepTime(start)
	assert.Contains(t, additions, config.SDKKey("extra1"))

	// After the grace expiry, the old primary should be revoked.
	additions, expirations := rotator.StepTime(start.Add(1*time.Hour + time.Millisecond))
	assert.Empty(t, additions)
	assert.Contains(t, expirations, config.SDKKey("primary"))
}

func TestPromotingExpiringAdditionalSDKKeyToPrimaryRemovesFromExpiringSet(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})

	expiry := time.Unix(100000, 0)
	rotator.SetAdditionalSDKKeys(nil, map[config.SDKKey]time.Time{"expiring1": expiry})
	_, _ = rotator.StepTime(time.Unix(1000, 0))

	// Now the platform decides to promote the expiring additional key to primary -- it stops
	// being deprecated and becomes the new primary.
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("expiring1"),
		NewGracePeriod(config.SDKKey("primary"), start.Add(1*time.Hour), start),
	)

	assert.Equal(t, config.SDKKey("expiring1"), rotator.SDKKey())
	// The previously-expiring entry should no longer appear in the deprecated list as an additional;
	// only the old primary should be there now.
	deprecated := rotator.DeprecatedCredentials()
	assert.Contains(t, deprecated, config.SDKKey("primary"))
	assert.NotContains(t, deprecated, config.SDKKey("expiring1"))
}

// Regression test for the F4 finding: a self-rotation -- RotateWithGrace(P, grace{P, ...}) --
// must NOT enter the deprecation flow. The unconditional addCredential -> startSDKClient that
// would otherwise result overwrites c.clients[P] and leaks the running upstream client.
func TestRotateWithGraceIgnoresSelfRotation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary")})
	_, _ = rotator.StepTime(time.Now())

	// Rotate the primary to itself with a grace period for itself.
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("primary"),
		NewGracePeriod(config.SDKKey("primary"), start.Add(1*time.Hour), start),
	)

	// Nothing should be queued -- the rotation is a no-op.
	additions, expirations := rotator.StepTime(start)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	// Primary remains the same and is NOT in the deprecated set.
	assert.Equal(t, config.SDKKey("primary"), rotator.SDKKey())
	assert.NotContains(t, rotator.DeprecatedCredentials(), config.SDKKey("primary"))
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "matches the current primary key")
}

func TestRotateMobileKeyWithGraceIgnoresSelfRotation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})
	_, _ = rotator.StepTime(time.Now())

	start := time.Unix(10000, 0)
	rotator.RotateMobileKeyWithGrace(
		config.MobileKey("primary"),
		NewMobileGracePeriod(config.MobileKey("primary"), start.Add(1*time.Hour), start),
	)

	additions, expirations := rotator.StepTime(start)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
	assert.Equal(t, config.MobileKey("primary"), rotator.MobileKey())
	assert.NotContains(t, rotator.DeprecatedCredentials(), config.MobileKey("primary"))
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "matches the current primary key")
}

// Regression test for the F2 finding from multi-agent review: a SetAdditional patch that omits a
// key currently in rotation grace must NOT revoke that key. The rotation grace flow owns the
// key's lifecycle; the diff loop must leave it alone.
func TestSetAdditionalOmissionDoesNotRevokeRotationPredecessor(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary-v1")})

	// Patch 1: register "A" as an additional key.
	rotator.SetAdditionalSDKKeys([]config.SDKKey{"A"}, nil)
	_, _ = rotator.StepTime(time.Now())

	// Patch 2: rotation arrives where "A" is now the expiring predecessor (grace.key = A) and a
	// new primary is "primary-v2". The autoconfig path runs UpdateCredential first, then
	// SetAdditional with an empty additional list.
	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("primary-v2"),
		NewGracePeriod(config.SDKKey("A"), start.Add(1*time.Hour), start),
	)
	rotator.SetAdditionalSDKKeys(nil, nil)

	additions, expirations := rotator.StepTime(start)
	// "primary-v2" is queued as the new primary. "A" should NOT be expired -- it just transitioned
	// from additional to rotation-predecessor and still has its full grace window.
	assert.Contains(t, additions, config.SDKKey("primary-v2"))
	assert.NotContains(t, expirations, config.SDKKey("A"))
	assert.Contains(t, rotator.DeprecatedCredentials(), config.SDKKey("A"))
}

// Regression test for the F3 finding: after a rotation grace expires for a key that was previously
// an additional, the additional set must not retain a phantom entry.
func TestRotationPredecessorClearedFromAdditionalSet(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.SDKKey("primary-v1")})

	rotator.SetAdditionalSDKKeys([]config.SDKKey{"A"}, nil)
	_, _ = rotator.StepTime(time.Now())

	start := time.Unix(10000, 0)
	rotator.RotateWithGrace(
		config.SDKKey("primary-v2"),
		NewGracePeriod(config.SDKKey("A"), start.Add(1*time.Hour), start),
	)
	_, _ = rotator.StepTime(start)

	// After the grace expires, the rotator should not still report "A" as an active additional.
	_, _ = rotator.StepTime(start.Add(1*time.Hour + time.Millisecond))
	assert.NotContains(t, rotator.ActiveSDKKeys(), config.SDKKey("A"))
}

// Same scenarios for mobile keys.
func TestSetAdditionalOmissionDoesNotRevokeMobileRotationPredecessor(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary-v1")})

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"A"}, nil)
	_, _ = rotator.StepTime(time.Now())

	start := time.Unix(10000, 0)
	rotator.RotateMobileKeyWithGrace(
		config.MobileKey("primary-v2"),
		NewMobileGracePeriod(config.MobileKey("A"), start.Add(1*time.Hour), start),
	)
	rotator.SetAdditionalMobileKeys(nil, nil)

	additions, expirations := rotator.StepTime(start)
	assert.Contains(t, additions, config.MobileKey("primary-v2"))
	assert.NotContains(t, expirations, config.MobileKey("A"))
	assert.Contains(t, rotator.DeprecatedCredentials(), config.MobileKey("A"))
}

func TestMobileRotationPredecessorClearedFromAdditionalSet(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary-v1")})

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"A"}, nil)
	_, _ = rotator.StepTime(time.Now())

	start := time.Unix(10000, 0)
	rotator.RotateMobileKeyWithGrace(
		config.MobileKey("primary-v2"),
		NewMobileGracePeriod(config.MobileKey("A"), start.Add(1*time.Hour), start),
	)
	_, _ = rotator.StepTime(start)
	_, _ = rotator.StepTime(start.Add(1*time.Hour + time.Millisecond))

	assert.NotContains(t, rotator.ActiveMobileKeys(), config.MobileKey("A"))
}

// --- Mobile-key additional-set tests (parallel to the SDK suite above) ---

func TestSetAdditionalMobileKeysAddsActiveKeys(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})
	_, _ = rotator.StepTime(time.Now())

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"extra1", "extra2"}, nil)
	additions, expirations := rotator.StepTime(time.Now())

	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("extra1"), config.MobileKey("extra2")}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.MobileKey{"primary", "extra1", "extra2"}, rotator.ActiveMobileKeys())
}

func TestSetAdditionalMobileKeysAddsExpiringKeys(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})
	_, _ = rotator.StepTime(time.Now())

	expiry := time.Unix(2000, 0)
	rotator.SetAdditionalMobileKeys(nil, map[config.MobileKey]time.Time{"expiring1": expiry})
	additions, expirations := rotator.StepTime(time.Unix(1000, 0))

	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("expiring1")}, additions)
	assert.Empty(t, expirations)
	assert.ElementsMatch(t, []config.MobileKey{"primary"}, rotator.ActiveMobileKeys())
	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("expiring1")}, rotator.DeprecatedCredentials())
}

func TestSetAdditionalMobileKeysOmissionRevokesImmediately(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"extra1", "extra2"}, nil)
	_, _ = rotator.StepTime(time.Now())

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"extra1"}, nil)
	additions, expirations := rotator.StepTime(time.Now())

	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("extra2")}, expirations)
}

func TestSetAdditionalMobileKeysActiveToExpiringStaysMapped(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"k"}, nil)
	_, _ = rotator.StepTime(time.Now())

	expiry := time.Unix(5000, 0)
	rotator.SetAdditionalMobileKeys(nil, map[config.MobileKey]time.Time{"k": expiry})
	additions, expirations := rotator.StepTime(time.Unix(1000, 0))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)
}

func TestSetAdditionalMobileKeysAcceptsUpdatedExpiry(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})

	earlyExpiry := time.Unix(2000, 0)
	rotator.SetAdditionalMobileKeys(nil, map[config.MobileKey]time.Time{"k": earlyExpiry})
	_, _ = rotator.StepTime(time.Unix(1000, 0))

	lateExpiry := time.Unix(10000, 0)
	rotator.SetAdditionalMobileKeys(nil, map[config.MobileKey]time.Time{"k": lateExpiry})

	additions, expirations := rotator.StepTime(time.Unix(5000, 0))
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	additions, expirations = rotator.StepTime(time.Unix(11000, 0))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("k")}, expirations)
}

func TestSetAdditionalMobileKeysFiltersPrimary(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary")})

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"primary", "extra"}, nil)
	additions, _ := rotator.StepTime(time.Now())

	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("extra")}, additions)
	assert.ElementsMatch(t, []config.MobileKey{"primary", "extra"}, rotator.ActiveMobileKeys())
}

// --- RotateMobileKeyWithGrace tests ---

func TestRotateMobileKeyWithGraceDeprecatesPrevious(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	key1 := config.MobileKey("mob-v1")
	key2 := config.MobileKey("mob-v2")

	start := time.Unix(10000, 0)
	halftime := start.Add(30 * time.Minute)
	deprecationTime := start.Add(1 * time.Hour)

	rotator.Initialize([]SDKCredential{key1})
	rotator.RotateMobileKeyWithGrace(key2, NewMobileGracePeriod(key1, deprecationTime, start))

	additions, expirations := rotator.StepTime(halftime)
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.Empty(t, expirations)

	// Before expiry: still valid.
	additions, expirations = rotator.StepTime(deprecationTime)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	// After expiry: predecessor gets revoked.
	additions, expirations = rotator.StepTime(deprecationTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
}

func TestRotateMobileKeyWithGraceNilImmediatelyRevokes(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	key1 := config.MobileKey("mob-v1")
	key2 := config.MobileKey("mob-v2")

	rotator.Initialize([]SDKCredential{key1})
	rotator.RotateMobileKeyWithGrace(key2, nil)

	additions, expirations := rotator.StepTime(time.Now())
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
}

func TestRotateMobileKeyWithGraceExpiredInThePastIsNotAdded(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	primary := config.MobileKey("primary")
	obsolete := config.MobileKey("obsolete")
	obsoleteExpiry := time.Unix(1000000, 0)
	now := obsoleteExpiry.Add(1 * time.Hour)

	rotator.RotateMobileKeyWithGrace(primary, NewMobileGracePeriod(obsolete, obsoleteExpiry, now))

	additions, expirations := rotator.StepTime(now)
	assert.ElementsMatch(t, []SDKCredential{primary}, additions)
	assert.Empty(t, expirations)
}

func TestSetAdditionalMobileKeysDoesNotDisturbRotationPredecessor(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	rotator.Initialize([]SDKCredential{config.MobileKey("primary-v1")})

	start := time.Unix(10000, 0)
	rotator.RotateMobileKeyWithGrace(
		config.MobileKey("primary-v2"),
		NewMobileGracePeriod(config.MobileKey("primary-v1"), start.Add(1*time.Hour), start),
	)
	_, _ = rotator.StepTime(start)

	rotator.SetAdditionalMobileKeys([]config.MobileKey{"extra"}, nil)
	additions, expirations := rotator.StepTime(start.Add(1 * time.Minute))
	assert.ElementsMatch(t, []SDKCredential{config.MobileKey("extra")}, additions)
	assert.Empty(t, expirations)
	assert.Contains(t, rotator.DeprecatedCredentials(), config.MobileKey("primary-v1"))
}
