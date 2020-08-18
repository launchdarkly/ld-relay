package config

import (
	"crypto/tls"
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

func TestOptTLSVersion(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		o := OptTLSVersion{}
		assert.False(t, o.IsDefined())
		assert.Equal(t, uint16(0), o.Get())
	})

	t.Run("new from valid string", func(t *testing.T) {
		for _, val := range []struct {
			s string
			n uint16
		}{{"1.0", tls.VersionTLS10}, {"1.1", tls.VersionTLS11}, {"1.2", tls.VersionTLS12}, {"1.3", tls.VersionTLS13}} {
			t.Run(val.s, func(t *testing.T) {
				o, err := NewOptTLSVersionFromString(val.s)
				assert.NoError(t, err)
				assert.True(t, o.IsDefined())
				assert.Equal(t, val.n, o.Get())
			})
		}
	})

	t.Run("new from empty string", func(t *testing.T) {
		o, err := NewOptTLSVersionFromString("")
		assert.NoError(t, err)
		assert.Equal(t, OptTLSVersion{}, o)
	})

	t.Run("new from invalid string", func(t *testing.T) {
		for _, s := range []string{"x", "1.4"} {
			o, err := NewOptTLSVersionFromString(s)
			assert.Equal(t, errBadTLSVersion(s), err)
			assert.Equal(t, OptTLSVersion{}, o)
		}
	})

	t.Run("get string value", func(t *testing.T) {
		assert.Equal(t, "", OptTLSVersion{}.String())
		assert.Equal(t, "1.0", NewOptTLSVersion(tls.VersionTLS10).String())
		assert.Equal(t, "1.1", NewOptTLSVersion(tls.VersionTLS11).String())
		assert.Equal(t, "1.2", NewOptTLSVersion(tls.VersionTLS12).String())
		assert.Equal(t, "1.3", NewOptTLSVersion(tls.VersionTLS13).String())
		assert.Equal(t, "unknown (9999)", NewOptTLSVersion(9999).String())
	})
}
