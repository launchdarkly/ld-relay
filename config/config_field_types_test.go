package config

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestSDKCredential(t *testing.T) {
	assert.Equal(t, "123", SDKKey("123").GetAuthorizationHeaderValue())
	assert.Equal(t, "456", MobileKey("456").GetAuthorizationHeaderValue())
	assert.Equal(t, "", EnvironmentID("123").GetAuthorizationHeaderValue())
}

func TestOptAbsoluteURL(t *testing.T) {
	absoluteURLString := "http://absolute/url"
	absoluteURL, _ := url.Parse(absoluteURLString)
	relativeURLString := "relative/url"
	relativeURL, _ := url.Parse(relativeURLString)
	malformedURLString := "::"

	t.Run("zero value", func(t *testing.T) {
		o := OptAbsoluteURL{}
		assert.False(t, o.IsDefined())
		assert.Nil(t, o.Get())
		assert.Equal(t, "", o.String())
		assert.Equal(t, "x", o.StringOrElse("x"))
	})

	t.Run("new from valid URL", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromURL(absoluteURL)
		assert.NoError(t, err)
		assert.True(t, o.IsDefined())
		assert.Equal(t, absoluteURL, o.Get())
		assert.Equal(t, absoluteURLString, o.String())
		assert.Equal(t, absoluteURLString, o.StringOrElse("x"))
	})

	t.Run("new from valid URL string", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromString(absoluteURLString)
		assert.NoError(t, err)
		assert.True(t, o.IsDefined())
		assert.Equal(t, absoluteURL, o.Get())
		assert.Equal(t, absoluteURLString, o.String())
		assert.Equal(t, absoluteURLString, o.StringOrElse("x"))
	})

	t.Run("new from nil URL", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromURL(nil)
		assert.NoError(t, err)
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("new from empty string", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromString("")
		assert.NoError(t, err)
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("new from relative URL", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromURL(relativeURL)
		assert.Equal(t, errNotAbsoluteURL(), err)
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("new from relative URL string", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromString(relativeURLString)
		assert.Equal(t, errNotAbsoluteURL(), err)
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("new from malformed URL string", func(t *testing.T) {
		o, err := NewOptAbsoluteURLFromString(malformedURLString)
		assert.Equal(t, errBadURLString(), err)
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("parse from valid URL string", func(t *testing.T) {
		var o OptAbsoluteURL
		assert.NoError(t, o.UnmarshalText([]byte(absoluteURLString)))
		assert.Equal(t, absoluteURL, o.Get())
	})

	t.Run("parse from empty string", func(t *testing.T) {
		var o OptAbsoluteURL
		assert.NoError(t, o.UnmarshalText([]byte("")))
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("parse from relative URL string", func(t *testing.T) {
		var o OptAbsoluteURL
		assert.Equal(t, errNotAbsoluteURL(), o.UnmarshalText([]byte(relativeURLString)))
		assert.Equal(t, OptAbsoluteURL{}, o)
	})

	t.Run("parse from malformed URL string", func(t *testing.T) {
		var o OptAbsoluteURL
		assert.Equal(t, errBadURLString(), o.UnmarshalText([]byte(malformedURLString)))
		assert.Equal(t, OptAbsoluteURL{}, o)
	})
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
