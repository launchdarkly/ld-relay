package credential

import (
	"fmt"
	"testing"
	"time"

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
		// so it should not appear as an expiration here. Cleanup is deferred to T1.c.
		additions, expirations := rotator.StepTime(start)
		assert.ElementsMatch(t, []SDKCredential{mob2}, additions)
		assert.Empty(t, expirations)
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
	})
}
