package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestSDKCredential(t *testing.T) {
	assert.Equal(t, "123", SDKKey("123").GetAuthorizationHeaderValue())
	assert.Equal(t, "456", MobileKey("456").GetAuthorizationHeaderValue())
	assert.Equal(t, "", EnvironmentID("123").GetAuthorizationHeaderValue())
}

func TestOptLogLevel(t *testing.T) {
	validLevel := ldlog.Warn
	validString := "wArN"
	badString := "wrong"

	t.Run("zero value", func(t *testing.T) {
		o := OptLogLevel{}
		assert.False(t, o.IsDefined())
		assert.Equal(t, ldlog.Error, o.GetOrElse(ldlog.Error))
	})

	t.Run("new from valid string", func(t *testing.T) {
		o, err := NewOptLogLevelFromString(validString)
		assert.NoError(t, err)
		assert.True(t, o.IsDefined())
		assert.Equal(t, validLevel, o.GetOrElse(ldlog.Error))
	})

	t.Run("new from empty string", func(t *testing.T) {
		o, err := NewOptLogLevelFromString("")
		assert.NoError(t, err)
		assert.Equal(t, OptLogLevel{}, o)
	})

	t.Run("new from invalid string", func(t *testing.T) {
		o, err := NewOptLogLevelFromString(badString)
		assert.Equal(t, errBadLogLevel(badString), err)
		assert.Equal(t, OptLogLevel{}, o)
	})
}
