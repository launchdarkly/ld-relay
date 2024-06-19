package credential

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func requireChanValue(t *testing.T, ch <-chan SDKCredential, expected SDKCredential) {
	t.Helper()
	value := helpers.RequireValue(t, ch, 1*time.Second)
	require.Equal(t, expected, value)
}

func TestNewRotator(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers, time.Now)
	assert.NotNil(t, rotator)
}

func TestKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers, time.Now)

	const (
		key1 = config.SDKKey("key1")
		key2 = config.SDKKey("key2")
		key3 = config.SDKKey("key3")
	)

	// The first rotation shouldn't trigger any expirations because there was no previous key.
	rotator.RotateSDKKey(key1, nil)
	requireChanValue(t, rotator.Additions(), key1)
	assert.Equal(t, key1, rotator.SDKKey())

	// The second rotation should trigger a deprecation of key1.
	rotator.RotateSDKKey(key2, nil)
	requireChanValue(t, rotator.Additions(), key2)
	requireChanValue(t, rotator.Expirations(), key1)
	assert.Equal(t, key2, rotator.SDKKey())

	// The third rotation should trigger a deprecation of key2.
	rotator.RotateSDKKey(key3, nil)
	requireChanValue(t, rotator.Additions(), key3)
	requireChanValue(t, rotator.Expirations(), key2)
	assert.Equal(t, key3, rotator.SDKKey())
}

func TestMobileKeyDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers, time.Now)

	const (
		key1 = config.MobileKey("key1")
		key2 = config.MobileKey("key2")
		key3 = config.MobileKey("key3")
	)

	rotator.RotateMobileKey(key1)
	requireChanValue(t, rotator.Additions(), key1)
	assert.Equal(t, key1, rotator.MobileKey())

	rotator.RotateMobileKey(key2)
	requireChanValue(t, rotator.Additions(), key2)
	requireChanValue(t, rotator.Expirations(), key1)
	assert.Equal(t, key2, rotator.MobileKey())

	rotator.RotateMobileKey(key3)
	requireChanValue(t, rotator.Additions(), key3)
	requireChanValue(t, rotator.Expirations(), key2)
	assert.Equal(t, key3, rotator.MobileKey())
}
