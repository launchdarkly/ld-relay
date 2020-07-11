package config

import (
	"net/url"
	"testing"
	"time"

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

func TestOptLogDuration(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		o1 := OptDuration{}
		assert.False(t, o1.IsDefined())
		assert.Equal(t, time.Hour, o1.GetOrElse(time.Hour))

		o2, err := NewOptDurationFromString("")
		assert.NoError(t, err)
		assert.Equal(t, o1, o2)

		var o3 OptDuration
		assert.NoError(t, o3.UnmarshalText([]byte("")))
		assert.Equal(t, o1, o3)
	})

	t.Run("valid strings", func(t *testing.T) {
		testValue := func(input string, result time.Duration) {
			t.Run(input, func(t *testing.T) {
				o1, err := NewOptDurationFromString(input)
				if assert.NoError(t, err) {
					assert.Equal(t, result, o1.GetOrElse(0))
				}

				var o2 OptDuration
				err = o2.UnmarshalText([]byte(input))
				if assert.NoError(t, err) {
					assert.Equal(t, result, o2.GetOrElse(0))
				}
			})
		}
		testValue("3ms", 3*time.Millisecond)
		testValue("3s", 3*time.Second)
		testValue("3m", 3*time.Minute)
		testValue("3h", 3*time.Hour)
		testValue(":30", 30*time.Second)
		testValue("1:30", time.Minute+30*time.Second)
		testValue("1:10:30", time.Hour+10*time.Minute+30*time.Second)
	})

	t.Run("invalid strings", func(t *testing.T) {
		testValue := func(input string) {
			t.Run(input, func(t *testing.T) {
				_, err := NewOptDurationFromString(input)
				assert.Equal(t, errBadDuration(input), err)

				var o2 OptDuration
				err = o2.UnmarshalText([]byte(input))
				assert.Equal(t, errBadDuration(input), err)
			})
		}
		testValue("1")
		testValue("1x")
		testValue("00:30:")
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
