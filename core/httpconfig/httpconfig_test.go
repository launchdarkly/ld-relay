package httpconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestUserAgentHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, nil, "abc", ldlog.NewDefaultLoggers())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.GetDefaultHeaders()
	assert.Contains(t, headers.Get("User-Agent"), "abc")
}

func TestNoAuthorizationHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDefaultLoggers())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.GetDefaultHeaders()
	assert.Equal(t, "", headers.Get("Authorization"))
}

func TestAuthorizationHeader(t *testing.T) {
	hc, err := NewHTTPConfig(config.ProxyConfig{}, config.SDKKey("key"), "", ldlog.NewDefaultLoggers())
	require.NoError(t, err)
	require.NotNil(t, hc)
	headers := hc.SDKHTTPConfig.GetDefaultHeaders()
	assert.Equal(t, "key", headers.Get("Authorization"))
}
