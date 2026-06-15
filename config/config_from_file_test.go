package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	helpers "github.com/launchdarkly/go-test-helpers/v3"
)

func TestConfigFromFileWithValidProperties(t *testing.T) {
	for _, tdc := range makeValidConfigs() {
		if tdc.fileContent == "" {
			// some tests only apply to environment variables, not files
			continue
		}
		t.Run(tdc.name, func(t *testing.T) {
			testFileWithValidConfig(t, tdc)
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
	t.Run("raises error for unknown config section", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Unknown]
`,
			`unsupported or misspelled section "Unknown"`,
		)
	})

	t.Run("raises error for unknown config field", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
Unknown = x`,
			`unsupported or misspelled section "Main", variable "Unknown"`,
		)
	})

	t.Run("allows boolean values 0/1 or true/false", func(t *testing.T) {
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = true },
			fileContent: `[Main]
ExitOnError = true`,
		})
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = true },
			fileContent: `[Main]
ExitOnError = 1`,
		})
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = false },
			fileContent: `[Main]
ExitOnError = false`,
		})
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.ExitOnError = false },
			fileContent: `[Main]
ExitOnError = 0`,
		})
	})

	t.Run("rejects invalid boolean value", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
ExitOnError = "x"`,
			"failed to parse bool `x`",
		)
	})

	t.Run("parses valid int", func(t *testing.T) {
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.Port = mustOptIntGreaterThanZero(222) },
			fileContent: `[Main]
Port = 222`,
		})
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
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.StreamURI = newOptURLAbsoluteMustBeValid("http://some/uri") },
			fileContent: `[Main]
StreamUri = "http://some/uri"`,
		})
	})

	t.Run("rejects invalid URI", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
StreamUri = "::"`,
			"not a valid URL/URI",
		)
		testFileWithInvalidConfig(t,
			`[Main]
StreamUri = "not/absolute"`,
			"must be an absolute URL/URI",
		)
	})

	t.Run("parses valid duration", func(t *testing.T) {
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.HeartbeatInterval = ct.NewOptDuration(3 * time.Second) },
			fileContent: `[Main]
HeartbeatInterval = 3s`,
		})
	})

	t.Run("rejects invalid duration", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
HeartbeatInterval = "x"`,
			"not a valid duration",
		)
	})

	t.Run("parses valid log level", func(t *testing.T) {
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.LogLevel = NewOptLogLevel(ldlog.Warn) },
			fileContent: `[Main]
LogLevel = "warn"`,
		})
		testFileWithValidConfig(t, testDataValidConfig{
			makeConfig: func(c *Config) { c.Main.LogLevel = NewOptLogLevel(ldlog.Error) },
			fileContent: `[Main]
LogLevel = "eRrOr"`,
		})
	})

	t.Run("rejects invalid log level", func(t *testing.T) {
		testFileWithInvalidConfig(t,
			`[Main]
LogLevel = "wrong"`,
			`"wrong" is not a valid log level`,
		)
	})
}

func testFileWithValidConfig(t *testing.T, tdc testDataValidConfig) {
	helpers.WithTempFile(func(filename string) {
		require.NoError(t, os.WriteFile(filename, []byte(tdc.fileContent), 0))

		var c Config
		mockLog := ldlogtest.NewMockLog()
		err := LoadConfigFile(&c, filename, mockLog.Loggers)
		require.NoError(t, err)
		tdc.assertResult(t, c, mockLog)
	})
}

func testFileWithInvalidConfig(t *testing.T, fileContent string, errMessage string) {
	helpers.WithTempFile(func(filename string) {
		require.NoError(t, os.WriteFile(filename, []byte(fileContent), 0))

		var c Config
		err := LoadConfigFile(&c, filename, ldlog.NewDisabledLoggers())
		require.Error(t, err)
		assert.Contains(t, err.Error(), errMessage)
	})
}
