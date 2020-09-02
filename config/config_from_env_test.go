package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
)

func TestConfigFromEnvironmentWithValidProperties(t *testing.T) {
	for _, tdc := range makeValidConfigs() {
		t.Run(tdc.name, func(t *testing.T) {
			testValidConfigVars(t, tdc)
		})
	}
}

func TestConfigFromEnvironmentWithInvalidProperties(t *testing.T) {
	for _, tdc := range makeInvalidConfigs() {
		if len(tdc.envVars) != 0 {
			t.Run(tdc.name, func(t *testing.T) {
				testInvalidConfigVars(t, tdc.envVars, tdc.envVarsError)
			})
		}
	}
}

func TestConfigFromEnvironmentOverridesExistingSettings(t *testing.T) {
	t.Run("can add SDK key to existing environment", func(t *testing.T) {
		var startingConfig, expectedConfig Config
		startingConfig.Environment = map[string]*EnvConfig{
			"envname": &EnvConfig{
				Prefix: "p",
			},
		}
		vars := map[string]string{
			"LD_ENV_envname": "my-key",
		}
		expectedConfig.Environment = map[string]*EnvConfig{
			"envname": &EnvConfig{
				SDKKey: SDKKey("my-key"),
				Prefix: "p",
			},
		}
		withEnvironment(vars, func() {
			c := startingConfig
			err := LoadConfigFromEnvironment(&c, ldlog.NewDisabledLoggers())
			require.NoError(t, err)

			assert.Equal(t, expectedConfig, c)
		})
	})

	t.Run("can change REDIS_PORT when REDIS_HOST was set", func(t *testing.T) {
		var startingConfig, expectedConfig Config
		startingConfig.Redis.Host = "redishost"
		vars := map[string]string{
			"REDIS_PORT": "2222",
		}
		expectedConfig.Redis.URL = newOptURLAbsoluteMustBeValid("redis://redishost:2222")
		withEnvironment(vars, func() {
			c := startingConfig
			err := LoadConfigFromEnvironment(&c, ldlog.NewDisabledLoggers())
			require.NoError(t, err)

			assert.Equal(t, expectedConfig, c)
		})
	})

	t.Run("can change REDIS_HOST when REDIS_PORT was set", func(t *testing.T) {
		var startingConfig, expectedConfig Config
		startingConfig.Redis.Port = mustOptIntGreaterThanZero(2222)
		vars := map[string]string{
			"USE_REDIS":  "1",
			"REDIS_HOST": "redishost",
		}
		expectedConfig.Redis.URL = newOptURLAbsoluteMustBeValid("redis://redishost:2222")
		withEnvironment(vars, func() {
			c := startingConfig
			err := LoadConfigFromEnvironment(&c, ldlog.NewDisabledLoggers())
			require.NoError(t, err)

			assert.Equal(t, expectedConfig, c)
		})
	})
}

func TestConfigFromEnvironmentDisallowsObsoleteVariables(t *testing.T) {
	t.Run("EVENTS_SAMPLING_INTERVAL", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{
				"EVENTS_SAMPLING_INTERVAL": "5",
			},
			"EVENTS_SAMPLING_INTERVAL: this variable is no longer supported",
		)
	})

	t.Run("REDIS_TTL", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{
				"USE_REDIS": "1",
				"REDIS_TTL": "500",
			},
			"REDIS_TTL: this variable is no longer supported; use CACHE_TTL",
		)
	})

	t.Run("LD_TTL_MINUTES_env", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{
				"LD_ENV_envname":         "key",
				"LD_TTL_MINUTES_envname": "3",
			},
			"LD_TTL_MINUTES_envname: this variable is no longer supported; use LD_TTL_envname",
		)
	})
}

func TestConfigFromEnvironmentFieldValidation(t *testing.T) {
	t.Run("allows boolean values 0/1 or true/false", func(t *testing.T) {
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = true },
			envVars:    map[string]string{"EXIT_ON_ERROR": "true"},
		})
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = true },
			envVars:    map[string]string{"EXIT_ON_ERROR": "1"},
		})
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = false },
			envVars:    map[string]string{"EXIT_ON_ERROR": "false"},
		})
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = false },
			envVars:    map[string]string{"EXIT_ON_ERROR": "0"},
		})
	})

	t.Run("rejects invalid boolean", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{"EXIT_ON_ERROR": "not really"},
			"EXIT_ON_ERROR: not a valid boolean",
		)
	})

	t.Run("parses valid int", func(t *testing.T) {
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.Port = mustOptIntGreaterThanZero(222) },
			envVars:    map[string]string{"PORT": "222"},
		})
	})

	t.Run("rejects invalid int", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{"PORT": "not-numeric"},
			"PORT: not a valid integer",
		)
	})

	t.Run("rejects <=0 value for int that must be >0", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{"PORT": "0"},
			"PORT: value must be greater than zero",
		)
		testInvalidConfigVars(t,
			map[string]string{"PORT": "-1"},
			"PORT: value must be greater than zero",
		)
	})

	t.Run("parses valid URI", func(t *testing.T) {
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.BaseURI = newOptURLAbsoluteMustBeValid("http://some/uri") },
			envVars:    map[string]string{"BASE_URI": "http://some/uri"},
		})
	})

	t.Run("rejects invalid URI", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{"BASE_URI": "::"},
			"BASE_URI: not a valid URL/URI",
		)
		testInvalidConfigVars(t,
			map[string]string{"BASE_URI": "not/absolute"},
			"BASE_URI: must be an absolute URL/URI",
		)
	})

	t.Run("parses valid duration", func(t *testing.T) {
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.HeartbeatInterval = ct.NewOptDuration(3 * time.Second) },
			envVars:    map[string]string{"HEARTBEAT_INTERVAL": "3s"},
		})
	})

	t.Run("rejects invalid duration", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{"HEARTBEAT_INTERVAL": "x"},
			"HEARTBEAT_INTERVAL: not a valid duration",
		)
	})

	t.Run("parses valid log level", func(t *testing.T) {
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.LogLevel = NewOptLogLevel(ldlog.Warn) },
			envVars:    map[string]string{"LOG_LEVEL": "warn"},
		})
		testValidConfigVars(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.LogLevel = NewOptLogLevel(ldlog.Error) },
			envVars:    map[string]string{"LOG_LEVEL": "eRrOr"},
		})
	})

	t.Run("rejects invalid log level", func(t *testing.T) {
		testInvalidConfigVars(t,
			map[string]string{"LOG_LEVEL": "wrong"},
			`LOG_LEVEL: "wrong" is not a valid log level`,
		)
	})
}

func testValidConfigVars(t *testing.T, tdc testDataValidConfig) { //} buildConfig func(c *Config), vars map[string]string) {
	withEnvironment(tdc.envVars, func() {
		var c Config
		mockLog := ldlogtest.NewMockLog()
		err := LoadConfigFromEnvironment(&c, mockLog.Loggers)
		require.NoError(t, err)
		tdc.assertResult(t, c, mockLog)
	})
}

func testInvalidConfigVars(t *testing.T, vars map[string]string, errMessage string) {
	withEnvironment(vars, func() {
		var c Config
		err := LoadConfigFromEnvironment(&c, ldlog.NewDisabledLoggers())
		require.Error(t, err)
		assert.Contains(t, err.Error(), errMessage)
	})
}

func withEnvironment(vars map[string]string, action func()) {
	saved := make(map[string]string)
	for _, kv := range os.Environ() {
		p := strings.Index(kv, "=")
		saved[kv[:p]] = kv[p+1:]
	}
	defer func() {
		os.Clearenv()
		for k, v := range saved {
			os.Setenv(k, v)
		}
	}()
	os.Clearenv()
	for k, v := range vars {
		os.Setenv(k, v)
	}
	action()
}
