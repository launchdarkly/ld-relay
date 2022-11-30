package application

import (
	"io"
	"testing"

	helpers "github.com/launchdarkly/go-test-helpers/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadOptions(t *testing.T) {
	appName := "ld-relay"

	t.Run("default config file path", func(t *testing.T) {
		_, err := ReadOptions([]string{appName}, io.Discard)
		require.Error(t, err)
		assert.Equal(t, errConfigFileNotFound(DefaultConfigPath), err)
	})

	t.Run("allow missing file with default path", func(t *testing.T) {
		opts, err := ReadOptions([]string{appName, "--allow-missing-file"}, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "", opts.ConfigFile)
		assert.False(t, opts.UseEnvironment)
	})

	t.Run("custom config file", func(t *testing.T) {
		helpers.WithTempFile(func(filename string) {
			opts, err := ReadOptions([]string{appName, "--config", filename}, io.Discard)
			require.NoError(t, err)
			assert.Equal(t, filename, opts.ConfigFile)
			assert.False(t, opts.UseEnvironment)
			assert.Equal(t, "configuration file "+filename, opts.DescribeConfigSource())
		})
	})

	t.Run("environment only", func(t *testing.T) {
		opts, err := ReadOptions([]string{appName, "--from-env"}, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "", opts.ConfigFile)
		assert.True(t, opts.UseEnvironment)
		assert.Equal(t, "configuration from environment variables", opts.DescribeConfigSource())
	})

	t.Run("environment plus config file", func(t *testing.T) {
		helpers.WithTempFile(func(filename string) {
			opts, err := ReadOptions([]string{appName, "--config", filename, "--from-env"}, io.Discard)
			require.NoError(t, err)
			assert.Equal(t, filename, opts.ConfigFile)
			assert.True(t, opts.UseEnvironment)
			assert.Equal(t, "configuration file "+filename+" plus environment variables", opts.DescribeConfigSource())
		})
	})

	t.Run("invalid options", func(t *testing.T) {
		_, err := ReadOptions([]string{appName, "--unknown"}, io.Discard)
		assert.Error(t, err)
	})
}

func TestDescribeRelayVersion(t *testing.T) {
	assert.Equal(t, "1.2.3", DescribeRelayVersion("1.2.3"))
	assert.Equal(t, "1.2.3 (build 999)", DescribeRelayVersion("1.2.3+999"))
}
