package autoconfig

import (
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const (
	oldKey            = config.SDKKey("oldkey")
	briefExpiryMillis = 300
)

func makeEnvWithExpiringKey(fromEnv EnvironmentRep, oldKey config.SDKKey) EnvironmentRep {
	ret := fromEnv
	ret.SDKKey.Expiring = ExpiringKeyRep{
		Value:     oldKey,
		Timestamp: ldtime.UnixMillisNow() + briefExpiryMillis,
	}
	return ret
}

func makeEnvWithAlreadyExpiredKey(fromEnv EnvironmentRep, oldKey config.SDKKey) EnvironmentRep {
	ret := fromEnv
	ret.SDKKey.Expiring = ExpiringKeyRep{
		Value:     oldKey,
		Timestamp: ldtime.UnixMillisNow() - 1,
	}
	return ret
}

func expectOldKeyWillExpire(p streamManagerTestParams, envID config.EnvironmentID) {
	p.mockLog.AssertMessageMatch(p.t, true, ldlog.Warn, "Old SDK key ending in dkey .* will expire")
	assert.Len(p.t, p.mockLog.GetOutput(ldlog.Error), 0)

	msg := p.requireMessage()
	require.NotNil(p.t, msg.expired)
	assert.Equal(p.t, envID, msg.expired.envID)
	assert.Equal(p.t, oldKey, msg.expired.key)

	p.mockLog.AssertMessageMatch(p.t, true, ldlog.Warn, "Old SDK key ending in dkey .* has expired")
}

func expectNoKeyExpiryMessage(p streamManagerTestParams) {
	p.mockLog.AssertMessageMatch(p.t, false, ldlog.Warn, "Old SDK key .* will expire")
}

func TestExpiringKeyInPutMessage(t *testing.T) {
	envWithExpiringKey := makeEnvWithExpiringKey(testEnv1, oldKey)
	event := makePutEvent(envWithExpiringKey)
	streamManagerTest(t, &event, func(p streamManagerTestParams) {
		p.startStream()

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		assert.Equal(t, makeEnvironmentParams(envWithExpiringKey), *msg.add)
		assert.Equal(t, oldKey, msg.add.ExpiringSDKKey)

		expectOldKeyWillExpire(p, envWithExpiringKey.EnvID)
	})
}

func TestExpiringKeyInPatchAdd(t *testing.T) {
	envWithExpiringKey := makeEnvWithExpiringKey(testEnv1, oldKey)
	event := makePatchEvent(envWithExpiringKey)
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(event)

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		assert.Equal(t, makeEnvironmentParams(envWithExpiringKey), *msg.add)
		assert.Equal(t, oldKey, msg.add.ExpiringSDKKey)

		expectOldKeyWillExpire(p, envWithExpiringKey.EnvID)
	})
}

func TestExpiringKeyInPatchUpdate(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(makePatchEvent(testEnv1))

		_ = p.requireMessage()

		envWithExpiringKey := makeEnvWithExpiringKey(testEnv1, oldKey)
		envWithExpiringKey.Version++

		p.stream.Enqueue(makePatchEvent(envWithExpiringKey))

		msg := p.requireMessage()
		require.NotNil(t, msg.update)
		assert.Equal(t, makeEnvironmentParams(envWithExpiringKey), *msg.update)
		assert.Equal(t, oldKey, msg.update.ExpiringSDKKey)

		expectOldKeyWillExpire(p, envWithExpiringKey.EnvID)
	})
}

func TestExpiringKeyHasAlreadyExpiredInPutMessage(t *testing.T) {
	envWithExpiringKey := makeEnvWithAlreadyExpiredKey(testEnv1, oldKey)
	event := makePutEvent(envWithExpiringKey)
	streamManagerTest(t, &event, func(p streamManagerTestParams) {
		p.startStream()

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		assert.Equal(t, makeEnvironmentParams(testEnv1), *msg.add)
		assert.Equal(t, config.SDKKey(""), msg.add.ExpiringSDKKey)

		expectNoKeyExpiryMessage(p)
	})
}

func TestExpiringKeyHasAlreadyExpiredInPatchAdd(t *testing.T) {
	envWithExpiringKey := makeEnvWithAlreadyExpiredKey(testEnv1, oldKey)
	event := makePatchEvent(envWithExpiringKey)
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(event)

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		assert.Equal(t, makeEnvironmentParams(testEnv1), *msg.add)
		assert.Equal(t, config.SDKKey(""), msg.add.ExpiringSDKKey)

		expectNoKeyExpiryMessage(p)
	})
}

func TestExpiringKeyHasAlreadyExpiredInPatchUpdate(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(makePatchEvent(testEnv1))

		_ = p.requireMessage()

		envWithExpiringKey := makeEnvWithAlreadyExpiredKey(testEnv1, oldKey)
		envWithExpiringKey.Version++

		p.stream.Enqueue(makePatchEvent(envWithExpiringKey))

		msg := p.requireMessage()
		require.NotNil(t, msg.update)
		assert.Equal(t, makeEnvironmentParams(testEnv1), *msg.update)
		assert.Equal(t, config.SDKKey(""), msg.update.ExpiringSDKKey)

		expectNoKeyExpiryMessage(p)
	})
}
