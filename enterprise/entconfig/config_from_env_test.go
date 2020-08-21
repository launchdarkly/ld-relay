package entconfig

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigFromEnvironmentWithValidProperties(t *testing.T) {
	for _, tdc := range makeValidConfigs() {
		t.Run(tdc.name, func(t *testing.T) {
			testValidConfigVars(t, tdc.makeConfig, tdc.envVars)
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

func testValidConfigVars(t *testing.T, buildConfig func(c *EnterpriseConfig), vars map[string]string) {
	withEnvironment(vars, func() {
		var expectedConfig EnterpriseConfig
		buildConfig(&expectedConfig)

		var c EnterpriseConfig
		err := LoadConfigFromEnvironment(&c)
		require.NoError(t, err)

		assert.Equal(t, expectedConfig, c)
	})
}

func testInvalidConfigVars(t *testing.T, vars map[string]string, errMessage string) {
	withEnvironment(vars, func() {
		var c EnterpriseConfig
		err := LoadConfigFromEnvironment(&c)
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
