package credential

import (
	"fmt"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestNewRotator(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)
	assert.NotNil(t, rotator)
}

func TestImmediateKeyExpiration(t *testing.T) {
	t.Run("sdk keys", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		const (
			key1 = config.SDKKey("key1")
			key2 = config.SDKKey("key2")
			key3 = config.SDKKey("key3")
		)

		// The first rotation shouldn't trigger any expirations because there was no previous key.
		rotator.RotateSDKKey(key1, nil)
		additions, _ := rotator.Tick(time.Now())
		assert.ElementsMatch(t, []SDKCredential{key1}, additions)
		assert.Equal(t, key1, rotator.SDKKey())

		// The second rotation should trigger a deprecation of key1.
		rotator.RotateSDKKey(key2, nil)
		additions, expirations := rotator.Tick(time.Now())
		assert.ElementsMatch(t, []SDKCredential{key2}, additions)
		assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
		assert.Equal(t, key2, rotator.SDKKey())

		// The third rotation should trigger a deprecation of key2.
		rotator.RotateSDKKey(key3, nil)
		additions, expirations = rotator.Tick(time.Now())
		assert.ElementsMatch(t, []SDKCredential{key3}, additions)
		assert.ElementsMatch(t, []SDKCredential{key2}, expirations)
		assert.Equal(t, key3, rotator.SDKKey())
	})

	t.Run("mobile keys", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		const (
			key1 = config.MobileKey("key1")
			key2 = config.MobileKey("key2")
			key3 = config.MobileKey("key3")
		)

		// The first rotation shouldn't trigger any expirations because there was no previous key.
		rotator.RotateMobileKey(key1)
		additions, _ := rotator.Tick(time.Now())
		assert.ElementsMatch(t, []SDKCredential{key1}, additions)
		assert.Equal(t, key1, rotator.MobileKey())

		// The second rotation should trigger a deprecation of key1.
		rotator.RotateMobileKey(key2)
		additions, expirations := rotator.Tick(time.Now())
		assert.ElementsMatch(t, []SDKCredential{key2}, additions)
		assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
		assert.Equal(t, key2, rotator.MobileKey())

		// The third rotation should trigger a deprecation of key2.
		rotator.RotateMobileKey(key3)
		additions, expirations = rotator.Tick(time.Now())
		assert.ElementsMatch(t, []SDKCredential{key3}, additions)
		assert.ElementsMatch(t, []SDKCredential{key2}, expirations)
		assert.Equal(t, key3, rotator.MobileKey())
	})
}

func TestManyImmediateKeyExpirations(t *testing.T) {
	t.Run("sdk keys", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		const numKeys = 100
		for i := 0; i < numKeys; i++ {
			key := config.SDKKey(fmt.Sprintf("key%v", i))
			rotator.RotateSDKKey(key, nil)
		}

		assert.Equal(t, config.SDKKey(fmt.Sprintf("key%v", numKeys-1)), rotator.SDKKey())

		additions, expirations := rotator.Tick(time.Now())
		assert.Len(t, additions, numKeys)
		assert.Len(t, expirations, numKeys-1) // because the last key is still active
	})

	t.Run("mobile keys", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		rotator := NewRotator(mockLog.Loggers)

		const numKeys = 100
		for i := 0; i < numKeys; i++ {
			key := config.MobileKey(fmt.Sprintf("key%v", i))
			rotator.RotateMobileKey(key)
		}

		assert.Equal(t, config.MobileKey(fmt.Sprintf("key%v", numKeys-1)), rotator.MobileKey())

		additions, expirations := rotator.Tick(time.Now())
		assert.Len(t, additions, numKeys)
		assert.Len(t, expirations, numKeys-1) // because the last key is still active
	})
}

func TestSDKKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	const (
		key1 = config.SDKKey("key1")
		key2 = config.SDKKey("key2")
		key3 = config.SDKKey("key3")
	)

	start := time.Now()

	deprecationTime := start.Add(1 * time.Minute)
	halfTime := start.Add(30 * time.Second)

	rotator.Initialize([]SDKCredential{key1})

	rotator.RotateSDKKey(key2, NewDeprecationNotice(key1, deprecationTime))
	additions, expirations := rotator.Tick(halfTime)
	assert.ElementsMatch(t, []SDKCredential{key2}, additions)
	assert.Empty(t, expirations)

	additions, expirations = rotator.Tick(deprecationTime)
	assert.Empty(t, additions)
	assert.Empty(t, expirations)

	additions, expirations = rotator.Tick(deprecationTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, []SDKCredential{key1}, expirations)
}

func TestManyConcurrentSDKKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers)

	rotator.Initialize([]SDKCredential{config.SDKKey("key0")})

	const numKeys = 250
	deprecationTime := time.Now().Add(1 * time.Minute)

	var keysDeprecated []SDKCredential
	var keysAdded []SDKCredential

	for i := 0; i < numKeys; i++ {
		previousKey := config.SDKKey(fmt.Sprintf("key%v", i))
		nextKey := config.SDKKey(fmt.Sprintf("key%v", i+1))

		keysDeprecated = append(keysDeprecated, previousKey)
		keysAdded = append(keysAdded, nextKey)

		rotator.RotateSDKKey(nextKey, NewDeprecationNotice(previousKey, deprecationTime))
	}

	assert.Equal(t, keysAdded[len(keysAdded)-1], rotator.SDKKey())

	additions, expirations := rotator.Tick(deprecationTime)
	assert.ElementsMatch(t, keysAdded, additions)
	assert.Empty(t, expirations)

	additions, expirations = rotator.Tick(deprecationTime.Add(1 * time.Millisecond))
	assert.Empty(t, additions)
	assert.ElementsMatch(t, keysDeprecated, expirations)
}
