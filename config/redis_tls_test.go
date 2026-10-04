package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedisConfigTLSEnabled(t *testing.T) {
	assert.False(t, RedisConfig{}.TLSEnabled())
	assert.False(t, RedisConfig{URL: newOptURLAbsoluteMustBeValid("redis://host:6379")}.TLSEnabled())
	assert.True(t, RedisConfig{URL: newOptURLAbsoluteMustBeValid("redis://host:6379"), TLS: true}.TLSEnabled())
	assert.True(t, RedisConfig{URL: newOptURLAbsoluteMustBeValid("rediss://host:6379")}.TLSEnabled())
	assert.True(t, RedisConfig{URL: newOptURLAbsoluteMustBeValid("REDISS://host:6379")}.TLSEnabled())
}
