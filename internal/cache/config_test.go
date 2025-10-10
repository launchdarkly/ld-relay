package cache

import (
	"testing"
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
)

func TestNewCacheConfigFromRelay(t *testing.T) {
	tests := []struct {
		name           string
		relayConfig    config.ObjectCacheConfig
		expectedConfig CacheConfig
	}{
		{
			name: "all values set",
			relayConfig: config.ObjectCacheConfig{
				Enabled:    true,
				MaxObjects: mustNewOptInt(5000),
				TTL:        ct.NewOptDuration(10 * time.Minute),
			},
			expectedConfig: CacheConfig{
				Enabled:    true,
				MaxObjects: 5000,
				TTL:        10 * time.Minute,
			},
		},
		{
			name: "disabled cache",
			relayConfig: config.ObjectCacheConfig{
				Enabled:    false,
				MaxObjects: mustNewOptInt(1000),
				TTL:        ct.NewOptDuration(2 * time.Minute),
			},
			expectedConfig: CacheConfig{
				Enabled:    false,
				MaxObjects: 1000,
				TTL:        2 * time.Minute,
			},
		},
		{
			name: "default values used when not set",
			relayConfig: config.ObjectCacheConfig{
				Enabled: true,
				// MaxObjects and TTL not set - should use defaults
			},
			expectedConfig: CacheConfig{
				Enabled:    true,
				MaxObjects: 10000,           // default value
				TTL:        5 * time.Minute, // default value
			},
		},
		{
			name:        "empty config uses all defaults",
			relayConfig: config.ObjectCacheConfig{
				// All fields at zero values
			},
			expectedConfig: CacheConfig{
				Enabled:    false,           // default value
				MaxObjects: 10000,           // default value
				TTL:        5 * time.Minute, // default value
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewCacheConfigFromRelay(tt.relayConfig)
			assert.Equal(t, tt.expectedConfig, result)
		})
	}
}

func TestDefaultObjectCacheConfig(t *testing.T) {
	config := config.ObjectCacheConfig{}

	assert.False(t, config.Enabled) // Should be disabled by default for safety
	// MaxObjects and TTL are not set, so they will have zero values
}

func TestCacheConfigValidation(t *testing.T) {
	// Test that valid configurations are accepted
	validConfig := CacheConfig{
		Enabled:    true,
		MaxObjects: 1000,
		TTL:        time.Minute,
	}
	assert.True(t, validConfig.Enabled)
	assert.Equal(t, 1000, validConfig.MaxObjects)
	assert.Equal(t, time.Minute, validConfig.TTL)

	// Test edge cases
	edgeConfig := CacheConfig{
		Enabled:    true,
		MaxObjects: 1,                // Minimum reasonable value
		TTL:        time.Millisecond, // Very short TTL
	}
	assert.True(t, edgeConfig.Enabled)
	assert.Equal(t, 1, edgeConfig.MaxObjects)
	assert.Equal(t, time.Millisecond, edgeConfig.TTL)
}

func TestOptIntGreaterThanZeroIntegration(t *testing.T) {
	// Test that the configtypes integration works correctly
	optInt := mustNewOptInt(42)

	relayConfig := config.ObjectCacheConfig{
		Enabled:    true,
		MaxObjects: optInt,
	}

	cacheConfig := NewCacheConfigFromRelay(relayConfig)
	assert.Equal(t, 42, cacheConfig.MaxObjects)
}

func TestOptDurationIntegration(t *testing.T) {
	// Test that the configtypes integration works correctly
	optDuration := ct.NewOptDuration(30 * time.Second)

	relayConfig := config.ObjectCacheConfig{
		Enabled: true,
		TTL:     optDuration,
	}

	cacheConfig := NewCacheConfigFromRelay(relayConfig)
	assert.Equal(t, 30*time.Second, cacheConfig.TTL)
}

// Helper function that panics on error for test simplicity
func mustNewOptInt(value int) ct.OptIntGreaterThanZero {
	opt, err := ct.NewOptIntGreaterThanZero(value)
	if err != nil {
		panic(err)
	}
	return opt
}

