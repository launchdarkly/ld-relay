package metrics

import (
	"errors"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterExporters(t *testing.T) {
	t.Run("creates only enabled exporters", func(t *testing.T) {
		fakeDatadogType := &testExporterTypeImpl{
			name: "Datadog",
			checkEnabled: func(mc config.MetricsConfig) bool {
				return mc.Datadog.Enabled
			},
		}
		fakePrometheusType := &testExporterTypeImpl{
			name: "Prometheus",
			checkEnabled: func(mc config.MetricsConfig) bool {
				return mc.Prometheus.Enabled
			},
		}

		var mc config.MetricsConfig
		mc.Prometheus.Enabled = true
		mockLog := ldlogtest.NewMockLog()

		exporters, err := registerExporters([]exporterType{fakeDatadogType, fakePrometheusType},
			mc, mockLog.Loggers)
		require.Nil(t, err)
		assert.Len(t, exporters, 1)
		require.NotNil(t, exporters[fakePrometheusType])
		assert.Equal(t, fakePrometheusType, exporters[fakePrometheusType].(*testExporterImpl).exporterType)
		assert.True(t, exporters[fakePrometheusType].(*testExporterImpl).registered)

		assert.Len(t, mockLog.GetOutput(ldlog.Error), 0)
	})

	t.Run("returns error and closes any already-registered exporters if create fails", func(t *testing.T) {
		fakeTypeThatSucceeds := &testExporterTypeImpl{
			name: "GOOD",
		}
		fakeTypeThatFails := &testExporterTypeImpl{
			name:          "BAD",
			errorOnCreate: errors.New("MESSAGE"),
		}

		mockLog := ldlogtest.NewMockLog()
		exporters, err := registerExporters([]exporterType{fakeTypeThatSucceeds, fakeTypeThatFails},
			config.MetricsConfig{}, mockLog.Loggers)
		require.NotNil(t, err)
		assert.Len(t, exporters, 0)

		require.Len(t, fakeTypeThatSucceeds.created, 1)
		require.Len(t, fakeTypeThatFails.created, 0)
		assert.True(t, fakeTypeThatSucceeds.created[0].registered)
		assert.True(t, fakeTypeThatSucceeds.created[0].closed)

		assert.Equal(t, []string{"Error creating BAD metrics exporter: MESSAGE"}, mockLog.GetOutput(ldlog.Error))
	})

	t.Run("returns error and closes any already-registered exporters if register fails", func(t *testing.T) {
		fakeTypeThatSucceeds := &testExporterTypeImpl{
			name: "GOOD",
		}
		fakeTypeThatFails := &testExporterTypeImpl{
			name:            "BAD",
			errorOnRegister: errors.New("MESSAGE"),
		}

		mockLog := ldlogtest.NewMockLog()
		exporters, err := registerExporters([]exporterType{fakeTypeThatSucceeds, fakeTypeThatFails},
			config.MetricsConfig{}, mockLog.Loggers)
		require.NotNil(t, err)
		assert.Len(t, exporters, 0)

		require.Len(t, fakeTypeThatSucceeds.created, 1)
		require.Len(t, fakeTypeThatFails.created, 1)
		assert.True(t, fakeTypeThatSucceeds.created[0].registered)
		assert.True(t, fakeTypeThatSucceeds.created[0].closed)
		assert.False(t, fakeTypeThatFails.created[0].registered)

		assert.Equal(t, []string{"Error registering BAD metrics exporter: MESSAGE"}, mockLog.GetOutput(ldlog.Error))
	})
}

func TestCloseExporters(t *testing.T) {
	t.Run("closes all exporters", func(t *testing.T) {
		fakeType1 := &testExporterTypeImpl{name: "A"}
		fakeType2 := &testExporterTypeImpl{name: "B"}

		mockLog := ldlogtest.NewMockLog()
		exporters, err := registerExporters([]exporterType{fakeType1, fakeType2},
			config.MetricsConfig{}, mockLog.Loggers)
		require.Nil(t, err)
		assert.Len(t, exporters, 2)
		assert.Len(t, fakeType1.created, 1)
		assert.Len(t, fakeType2.created, 1)

		closeExporters(exporters, mockLog.Loggers)

		assert.True(t, fakeType1.created[0].closed)
		assert.True(t, fakeType2.created[0].closed)

		assert.Len(t, mockLog.GetOutput(ldlog.Error), 0)
	})

	t.Run("logs any close errors and continues closing the rest of the exporters", func(t *testing.T) {
		fakeType1 := &testExporterTypeImpl{name: "A", errorOnClose: errors.New("MESSAGE")}
		fakeType2 := &testExporterTypeImpl{name: "B"}

		mockLog := ldlogtest.NewMockLog()
		exporters, err := registerExporters([]exporterType{fakeType1, fakeType2},
			config.MetricsConfig{}, mockLog.Loggers)
		require.Nil(t, err)
		assert.Len(t, exporters, 2)
		assert.Len(t, fakeType1.created, 1)
		assert.Len(t, fakeType2.created, 1)

		closeExporters(exporters, mockLog.Loggers)

		assert.False(t, fakeType1.created[0].closed)
		assert.True(t, fakeType2.created[0].closed)

		assert.Equal(t, []string{"Error closing A metrics exporter: MESSAGE"}, mockLog.GetOutput(ldlog.Error))
	})
}

func TestGetPrefix(t *testing.T) {
	assert.Equal(t, "x", getPrefix("x"))
	assert.Equal(t, defaultMetricsPrefix, getPrefix(""))
}
