package autoconfig

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	oldKey            = config.SDKKey("oldkey")
	briefExpiryMillis = 300
)

func makeEnvWithExpiringKey(fromEnv envfactory.EnvironmentRep, oldKey config.SDKKey) envfactory.EnvironmentRep {
	ret := fromEnv
	ret.SDKKey.Expiring = envfactory.ExpiringKeyRep{
		Value:     oldKey,
		Timestamp: ldtime.UnixMillisNow() + briefExpiryMillis,
	}
	return ret
}

func makeEnvWithAlreadyExpiredKey(fromEnv envfactory.EnvironmentRep, oldKey config.SDKKey) envfactory.EnvironmentRep {
	ret := fromEnv
	ret.SDKKey.Expiring = envfactory.ExpiringKeyRep{
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
	event := makeEnvPutEvent(envWithExpiringKey)
	streamManagerTest(t, &event, func(p streamManagerTestParams) {
		p.startStream()

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		p.requireReceivedAllMessage()

		assert.Equal(t, envWithExpiringKey.ToParams(), *msg.add)
		assert.Equal(t, oldKey, msg.add.ExpiringSDKKey)

		expectOldKeyWillExpire(p, envWithExpiringKey.EnvID)
	})
}

func TestExpiringKeyInPatchAdd(t *testing.T) {
	envWithExpiringKey := makeEnvWithExpiringKey(testEnv1, oldKey)
	event := makePatchEnvEvent(envWithExpiringKey)
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(event)

		msg := p.requireMessage()
		require.NotNil(t, msg.add)

		assert.Equal(t, envWithExpiringKey.ToParams(), *msg.add)
		assert.Equal(t, oldKey, msg.add.ExpiringSDKKey)

		expectOldKeyWillExpire(p, envWithExpiringKey.EnvID)
	})
}

func TestExpiringKeyInPatchUpdate(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(makePatchEnvEvent(testEnv1))

		_ = p.requireMessage()

		envWithExpiringKey := makeEnvWithExpiringKey(testEnv1, oldKey)
		envWithExpiringKey.Version++

		p.stream.Enqueue(makePatchEnvEvent(envWithExpiringKey))

		msg := p.requireMessage()
		require.NotNil(t, msg.update)
		assert.Equal(t, envWithExpiringKey.ToParams(), *msg.update)
		assert.Equal(t, oldKey, msg.update.ExpiringSDKKey)

		expectOldKeyWillExpire(p, envWithExpiringKey.EnvID)
	})
}

func TestExpiringKeyHasAlreadyExpiredInPutMessage(t *testing.T) {
	envWithExpiringKey := makeEnvWithAlreadyExpiredKey(testEnv1, oldKey)
	event := makeEnvPutEvent(envWithExpiringKey)
	streamManagerTest(t, &event, func(p streamManagerTestParams) {
		p.startStream()

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		p.requireReceivedAllMessage()

		assert.Equal(t, testEnv1.ToParams(), *msg.add)
		assert.Equal(t, config.SDKKey(""), msg.add.ExpiringSDKKey)

		expectNoKeyExpiryMessage(p)
	})
}

func TestExpiringKeyHasAlreadyExpiredInPatchAdd(t *testing.T) {
	envWithExpiringKey := makeEnvWithAlreadyExpiredKey(testEnv1, oldKey)
	event := makePatchEnvEvent(envWithExpiringKey)
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(event)

		msg := p.requireMessage()
		require.NotNil(t, msg.add)
		assert.Equal(t, testEnv1.ToParams(), *msg.add)
		assert.Equal(t, config.SDKKey(""), msg.add.ExpiringSDKKey)

		expectNoKeyExpiryMessage(p)
	})
}

func TestExpiringKeyHasAlreadyExpiredInPatchUpdate(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		p.stream.Enqueue(makePatchEnvEvent(testEnv1))

		_ = p.requireMessage()

		envWithExpiringKey := makeEnvWithAlreadyExpiredKey(testEnv1, oldKey)
		envWithExpiringKey.Version++

		p.stream.Enqueue(makePatchEnvEvent(envWithExpiringKey))

		msg := p.requireMessage()
		require.NotNil(t, msg.update)
		assert.Equal(t, testEnv1.ToParams(), *msg.update)
		assert.Equal(t, config.SDKKey(""), msg.update.ExpiringSDKKey)

		expectNoKeyExpiryMessage(p)
	})
}
