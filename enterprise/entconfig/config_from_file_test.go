package entconfig

import (
	"io/ioutil"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func testFileWithValidConfig(t *testing.T, buildConfig func(c *EnterpriseConfig), fileContent string) {
	var expectedConfig EnterpriseConfig
	buildConfig(&expectedConfig)

	helpers.WithTempFile(func(filename string) {
		require.NoError(t, ioutil.WriteFile(filename, []byte(fileContent), 0))

		var c EnterpriseConfig
		err := LoadConfigFile(&c, filename, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Equal(t, expectedConfig, c)
	})
}

func testFileWithInvalidConfig(t *testing.T, fileContent string, errMessage string) {
	helpers.WithTempFile(func(filename string) {
		require.NoError(t, ioutil.WriteFile(filename, []byte(fileContent), 0))

		var c EnterpriseConfig
		err := LoadConfigFile(&c, filename, ldlog.NewDisabledLoggers())
		require.Error(t, err)
		assert.Contains(t, err.Error(), errMessage)
	})
}
