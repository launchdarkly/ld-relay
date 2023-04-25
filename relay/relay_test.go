package relay

import (
	"net/http/httptest"
	"testing"

	c "github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRelayRejectsConfigWithNoEnvironmentsInManualConfigMode(t *testing.T) {
	config := c.Config{}
	relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
	assert.Equal(t, errNoEnvironments, err)
	assert.Nil(t, relay)
}

func TestNewRelayAllowsConfigWithNoEnvironmentsIfAutoConfigKeyIsSet(t *testing.T) {
	stubStreamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()
	httphelpers.WithServer(stubStreamHandler, func(server *httptest.Server) {
		streamURI, _ := configtypes.NewOptURLAbsoluteFromString(server.URL)
		config := c.Config{
			Main: c.MainConfig{
				StreamURI: streamURI,
			},
			AutoConfig: c.AutoConfigConfig{
				Key: "x",
			},
		}
		relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
		require.NoError(t, err)
		defer relay.Close()
	})
}

func TestNewRelayAllowsConfigWithNoEnvironmentsIfFileDataSourceIsSet(t *testing.T) {
	config := c.Config{
		OfflineMode: c.OfflineModeConfig{
			FileDataSource: "x",
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)

	// There will be an error, since we don't actually have a data file, but it should not be a
	// configuration error.
	require.Error(t, err)
	assert.NotEqual(t, errNoEnvironments, err)
}

func TestNewRelayDisallowsFiltersWhenNoEnvironmentsSpecified(t *testing.T) {
	config := c.Config{
		Filters: map[string]*c.FiltersConfig{
			"proj": {
				Keys: configtypes.NewOptStringList([]string{"foo"}),
			},
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
}

func TestNewRelayDisallowsFiltersWhenProjKeyNotSpecified(t *testing.T) {
	config := c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "proj",
			},
			"b": {
				SDKKey: "234",
				// missing project key
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"proj": {
				Keys: configtypes.NewOptStringList([]string{"foo"}),
			},
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
}

func TestNewRelayDisallowsFiltersWithUnmatchedProjects(t *testing.T) {
	config := c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "proj",
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"notProj": {
				Keys: configtypes.NewOptStringList([]string{"foo"}),
			},
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
}

func TestMakeFilteredEnvironments_NoFilters(t *testing.T) {
	cfg := &c.Config{Environment: map[string]*c.EnvConfig{
		"a": {
			SDKKey: "123",
		},
		"b": {
			SDKKey: "234",
		},
	}}
	envs := makeFilteredEnvironments(cfg)
	for _, id := range []string{"a", "b"} {
		require.Contains(t, envs, id)
	}
}

func TestMakeFilteredEnvironments_OneFilter_OneEnvironment(t *testing.T) {
	cfg := &c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "proj",
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"proj": {Keys: configtypes.NewOptStringList([]string{"foo", "bar"})},
		}}
	envs := makeFilteredEnvironments(cfg)
	for _, id := range []string{"a", "a/foo", "a/bar"} {
		require.Contains(t, envs, id)
	}
}

func TestMakeFilteredEnvironments_ManyFilters_ManyEnvironments(t *testing.T) {
	cfg := &c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "projA",
			},
			"b": {
				SDKKey:  "123",
				ProjKey: "projA",
			},
			"c": {
				SDKKey:  "123",
				ProjKey: "projB",
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"projA": {Keys: configtypes.NewOptStringList([]string{"foo", "bar"})},
			"projB": {Keys: configtypes.NewOptStringList([]string{"baz"})},
		}}
	envs := makeFilteredEnvironments(cfg)
	for _, id := range []string{"a", "b", "c", "a/foo", "a/bar", "b/foo", "b/bar", "c/baz"} {
		assert.Contains(t, envs, id)
	}
}
