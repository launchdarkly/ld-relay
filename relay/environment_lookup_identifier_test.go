package relay

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnvironmentLookup_LookupByIdentifier tests the identifier-based lookup functionality
// for the status endpoint. These are unit tests focusing on the lookup logic itself.
func TestEnvironmentLookup_LookupByIdentifier(t *testing.T) {
	t.Run("lookup by environment ID", func(t *testing.T) {
		var cfg config.Config
		cfg.Environment = st.MakeEnvConfigs(st.EnvClientSide)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential

			// Lookup by environment ID should succeed
			result, found := lookup.LookupByIdentifier(string(st.EnvClientSide.Config.EnvID), "")
			require.True(t, found, "Expected to find environment by ID")
			assert.NotNil(t, result)
		})
	})

	t.Run("lookup by configured name", func(t *testing.T) {
		var cfg config.Config
		cfg.Environment = st.MakeEnvConfigs(st.EnvMain)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential

			// Lookup by configured name should succeed
			result, found := lookup.LookupByIdentifier(st.EnvMain.Name, "")
			require.True(t, found, "Expected to find environment by configured name")
			assert.NotNil(t, result)
			assert.Equal(t, st.EnvMain.Name, result.GetIdentifiers().ConfiguredName)
		})
	})

	t.Run("lookup not found", func(t *testing.T) {
		var cfg config.Config
		cfg.Environment = st.MakeEnvConfigs(st.EnvMain)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential

			// Lookup with non-existent identifier
			_, found := lookup.LookupByIdentifier("nonexistent", "")
			assert.False(t, found, "Expected not to find non-existent environment")
		})
	})

	t.Run("lookup with wrong filter key", func(t *testing.T) {
		var cfg config.Config
		cfg.Environment = st.MakeEnvConfigs(st.EnvClientSide)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential

			// Lookup with non-existent filter should fail
			_, found := lookup.LookupByIdentifier(string(st.EnvClientSide.Config.EnvID), "nonexistent-filter")
			assert.False(t, found, "Expected not to find environment with wrong filter")
		})
	})

	t.Run("lookup with spaces in name", func(t *testing.T) {
		var cfg config.Config
		envWithSpaces := st.EnvMain
		envWithSpaces.Name = "My Production Env"
		cfg.Environment = st.MakeEnvConfigs(envWithSpaces)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential

			// Lookup should work with exact name
			result, found := lookup.LookupByIdentifier("My Production Env", "")
			require.True(t, found, "Expected to find environment with spaces in name")
			assert.NotNil(t, result)
			assert.Equal(t, "My Production Env", result.GetIdentifiers().ConfiguredName)
		})
	})

	t.Run("delete environment removes from identifier indexes", func(t *testing.T) {
		var cfg config.Config
		cfg.Environment = st.MakeEnvConfigs(st.EnvClientSide)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential
			envID := st.EnvClientSide.Config.EnvID

			// Verify it's in the index
			_, found := lookup.LookupByIdentifier(string(envID), "")
			require.True(t, found, "Environment should be in envID index")

			// Delete environment by credential
			params := sdkauth.New(st.EnvClientSide.Config.SDKKey)
			deleted, ok := lookup.DeleteEnvironment(params)
			require.True(t, ok, "Expected delete to succeed")
			assert.NotNil(t, deleted)

			// Verify it's removed from index
			_, found = lookup.LookupByIdentifier(string(envID), "")
			assert.False(t, found, "Environment should be removed from envID index")
		})
	})

	t.Run("projKey/envKey pattern requires slash", func(t *testing.T) {
		var cfg config.Config
		cfg.Environment = st.MakeEnvConfigs(st.EnvMain)

		withStartedRelay(t, cfg, func(p relayTestParams) {
			lookup := p.relay.envsByCredential

			// Lookup without slash should not match anything (no projKey/envKey in manual config)
			_, found := lookup.LookupByIdentifier("myprojectproduction", "")
			assert.False(t, found, "Should not match non-existent identifier")

			// Lookup with slash should also not match (manual config doesn't have projKey/envKey)
			_, found = lookup.LookupByIdentifier("myproject/production", "")
			assert.False(t, found, "Should not match non-existent projKey/envKey")
		})
	})
}
