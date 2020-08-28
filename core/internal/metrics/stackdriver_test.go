package metrics

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	helpers "github.com/launchdarkly/go-test-helpers/v2"
	config "github.com/launchdarkly/ld-relay-config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const (
	fakeGoogleCredentials = `{
  "type": "authorized_user",
  "projectId": "test-project-id"
}`

	fakeInvalidGoogleCredentials = `{
  "type": "unsupported"
}`
)

func TestStackdriverExporterType(t *testing.T) {
	exporterType := stackdriverExporterType
	fakeProjectID := "test-project-id"

	t.Run("name", func(t *testing.T) {
		assert.Equal(t, "Stackdriver", exporterType.getName())
	})

	t.Run("included in allExporterTypes", func(t *testing.T) {
		assert.Contains(t, allExporterTypes(), exporterType)
	})

	t.Run("does not create exporter if Stackdriver is disabled", func(t *testing.T) {
		var mc config.MetricsConfig
		e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
		require.NoError(t, err)
		assert.Nil(t, e)
	})

	t.Run("creates exporter if Stackdriver is enabled", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Stackdriver.Enabled = true
		mc.Stackdriver.ProjectID = fakeProjectID
		withDefaultGoogleApplicationCredentials([]byte(fakeGoogleCredentials), func() {
			e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
			require.NoError(t, err)
			assert.NotNil(t, e)
			e.close()
		})
	})

	t.Run("returns error for invalid credentials", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Stackdriver.Enabled = true
		mc.Stackdriver.ProjectID = fakeProjectID
		withDefaultGoogleApplicationCredentials([]byte(fakeInvalidGoogleCredentials), func() {
			e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
			require.Error(t, err)
			assert.Nil(t, e)
		})
	})

	t.Run("registers exporter without errors", func(t *testing.T) {
		var mc config.MetricsConfig
		mc.Stackdriver.Enabled = true
		mc.Stackdriver.ProjectID = fakeProjectID
		withDefaultGoogleApplicationCredentials([]byte(fakeGoogleCredentials), func() {
			e, err := exporterType.createExporterIfEnabled(mc, ldlog.NewDisabledLoggers())
			require.NoError(t, err)
			assert.NotNil(t, e)
			defer e.close()
			assert.NoError(t, e.register())
		})
	})
}

func withDefaultGoogleApplicationCredentials(data []byte, action func()) {
	varName := "GOOGLE_APPLICATION_CREDENTIALS"
	helpers.WithTempFile(func(filename string) {
		ioutil.WriteFile(filename, data, 0644)
		oldVar := os.Getenv(varName)
		defer os.Setenv(varName, oldVar)
		os.Setenv(varName, filename)
		action()
	})
}
