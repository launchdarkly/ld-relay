package config

import (
	"io/ioutil"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	helpers "github.com/launchdarkly/go-test-helpers/v2"
)

func TestConfigFromFileWithValidProperties(t *testing.T) {
	for _, tdc := range makeValidConfigs() {
		if tdc.fileContent == "" {
			// some tests only apply to environment variables, not files
			continue
		}
		t.Run(tdc.name, func(t *testing.T) {
			testFileWithValidConfig(t, tdc.makeConfig, tdc.fileContent)
		})
	}
}

func TestConfigFromFileWithInvalidProperties(t *testing.T) {
	for _, tdc := range makeInvalidConfigs() {
		if tdc.fileContent == "" {
			// some tests only apply to environment variables, not files
			continue
		}
		t.Run(tdc.name, func(t *testing.T) {
			e := tdc.fileError
			if e == "" {
				e = tdc.envVarsError
			}
			testFileWithInvalidConfig(t, tdc.fileContent, e)
		})
	}
}

func TestConfigFromFileBasicValidation(t *testing.T) {
	t.Run("allows boolean values 0/1 or true/false", func(t *testing.T) {
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.ExitOnError = true },
			`[Main]
ExitOnError = true`,
		)
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.ExitOnError = true },
			`[Main]
ExitOnError = 1`,
		)
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.ExitOnError = false },
			`[Main]
ExitOnError = false`,
		)
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.ExitOnError = false },
			`[Main]
ExitOnError = 0`,
		)
	})

	t.Run("rejects invalid boolean value", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
ExitOnError = "x"`,
			"failed to parse bool `x`",
		)
	})

	t.Run("parses valid int", func(t *testing.T) {
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.Port = mustOptIntGreaterThanZero(222) },
			`[Main]
Port = 222`,
		)
	})

	t.Run("rejects invalid int", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
Port = "x"`,
			"not a valid integer",
		)
	})

	t.Run("rejects <=0 value for int that must be >0", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
Port = "0"`,
			"value must be greater than zero",
		)
		testFileWithInvalidConfig(t,
			`[Main]
Port = "-1"`,
			"value must be greater than zero",
		)
	})

	t.Run("parses valid URI", func(t *testing.T) {
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.BaseURI = newOptURLAbsoluteMustBeValid("http://some/uri") },
			`[Main]
BaseUri = "http://some/uri"`,
		)
	})

	t.Run("rejects invalid URI", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
BaseUri = "::"`,
			"not a valid URL/URI",
		)
		testFileWithInvalidConfig(t,
			`[Main]
BaseUri = "not/absolute"`,
			"must be an absolute URL/URI",
		)
	})

	t.Run("parses valid duration", func(t *testing.T) {
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.HeartbeatInterval = ct.NewOptDuration(3 * time.Second) },
			`[Main]
HeartbeatInterval = 3s`,
		)
	})

	t.Run("rejects invalid duration", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
HeartbeatInterval = "x"`,
			"not a valid duration",
		)
	})

	t.Run("parses valid log level", func(t *testing.T) {
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.LogLevel = NewOptLogLevel(ldlog.Warn) },
			`[Main]
LogLevel = "warn"`,
		)
		testFileWithValidConfig(t,
			func(c *Config) { c.Main.LogLevel = NewOptLogLevel(ldlog.Error) },
			`[Main]
LogLevel = "eRrOr"`,
		)
	})

	t.Run("rejects invalid log level", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
LogLevel = "wrong"`,
			`"wrong" is not a valid log level`,
		)
	})
}

func testFileWithValidConfig(t *testing.T, buildConfig func(c *Config), fileContent string) {
	var expectedConfig Config
	buildConfig(&expectedConfig)

	helpers.WithTempFile(func(filename string) {
		require.NoError(t, ioutil.WriteFile(filename, []byte(fileContent), 0))

		var c Config
		err := LoadConfigFile(&c, filename, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Equal(t, expectedConfig, c)
	})
}

func testFileWithInvalidConfig(t *testing.T, fileContent string, errMessage string) {
	helpers.WithTempFile(func(filename string) {
		require.NoError(t, ioutil.WriteFile(filename, []byte(fileContent), 0))

		var c Config
		err := LoadConfigFile(&c, filename, ldlog.NewDisabledLoggers())
		require.Error(t, err)
		assert.Contains(t, err.Error(), errMessage)
	})
}
