package relay

import (
	"strings"
	"sync"

	"github.com/launchdarkly/ld-relay/v9/config"

	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
)

// EnvironmentLookup is responsible for maintaining a mapping between incoming requests from SDKs, and
// outbound connections to LaunchDarkly.
//
// Complexity is present for two reasons:
//  1. A configured LaunchDarkly connection can be authenticated in one or more ways: SDK key, mobile key,
//     client-side environment ID.
//     This component must be able to accept any of these credentials and find the correct environment.
//  2. Payload filtering results in extra bookkeeping: if a payload filter is specified for a project,
//     then Relay must maintain individual streaming connections for each variant of environments within that project
//     (unfiltered, filter X, filter Y...).
//     These environments share all the same credentials and most of the configuration, but are fundamentally different
//     due to the filter key tacked onto the request URL.
//
// Because of these two issues, the lookup is based on a composite key: the combination of a credential and a filter.
// If there is no filter, then the filter component is an empty string.
//
// As an example, assume two environments are configured (envA and envB).
// Both are authenticated with an SDK key, mobile key, and environment ID.
//
// The map has 6 entries:
//
//	#1 {envA SDK key, ""}    ----v
//	#2 {envA mobile key, ""} --> envA[filter=""]
//	#3 {envA env-ID, ""}     ----^
//
//	#4 {envB SDK key, ""}    ----v
//	#5 {envB mobile key, ""} --> envB[filter=""]
//	#6 {envB env-ID, ""}     ----^
//
// Assume both environments belong to the same project, and then a filter "foo" is added to this project.
// Here's a diff, for a total of 12 entries:
//
//	+#7 {envA SDK key, "foo"}     ----v
//	+#8 {envA mobile key, "foo"}  --> envA[filter="foo"]
//	+#9 {envA env ID, "foo"}      ----^
//
//	+#10 {envB SDK key, "foo"}    ----v
//	+#11 {envB mobile key, "foo"} --> envB[filter="foo"]
//	+#12 {envB env-ID, "foo"}     ----^
//
// The relationship between envA[filter=""] and envA[filter="foo"] is that both environments share the
// exact same credentials, but the objects themselves represent distinct connections.
//
// As shown, given N environments in a project, and M filters for that project, then N*(M+1) environment connections are
// maintained: N=2, M=1, count = 2*(1+1) = 4.

// projEnvKey is a composite key for looking up environments by project key and environment key.
// This is only available in auto-config mode where environments have project and environment metadata.
type projEnvKey struct {
	projKey string
	envKey  string
}

type EnvironmentLookup struct {
	// mapping maps {credential, filter} keys to environment connections.
	mapping map[credential.SDKCredential]relayenv.EnvContext
	// conns is the set of unique environment connections
	conns map[relayenv.EnvContext]struct{}

	// Identifier-based indexes for status endpoint lookups.
	// Each environment can have multiple filter variants, so values are slices.
	// envIDIndex maps an environment ID to its environment (auto-config mode)
	envIDIndex map[config.EnvironmentID]relayenv.EnvContext
	// configNameIndex maps a configured name to its environment (manual config mode)
	configNameIndex map[string]relayenv.EnvContext
	// projEnvKeyIndex maps {projKey, envKey} to its environment (auto-config mode, human-readable)
	projEnvKeyIndex map[projEnvKey]relayenv.EnvContext

	// mu protects access to all maps
	mu sync.RWMutex
}

// NewEnvironmentLookup instantiates an empty instance of EnvironmentLookup. Calls into EnvironmentLookup
// are thread safe.
func NewEnvironmentLookup() *EnvironmentLookup {
	return &EnvironmentLookup{
		mapping:         make(map[credential.SDKCredential]relayenv.EnvContext),
		conns:           make(map[relayenv.EnvContext]struct{}),
		envIDIndex:      make(map[config.EnvironmentID]relayenv.EnvContext),
		configNameIndex: make(map[string]relayenv.EnvContext),
		projEnvKeyIndex: make(map[projEnvKey]relayenv.EnvContext),
	}
}

// InsertEnvironment creates a mapping from the given environment's credentials (and optional filter key)
// to that environment, which can later be looked up using Lookup.
// Only credentials that are defined are mapped (credential.Defined() must return true for each).
func (e *EnvironmentLookup) InsertEnvironment(env relayenv.EnvContext) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, cred := range env.GetCredentials() {
		if cred.Defined() {
			e.mapParams(cred, env)
		}
	}

	e.conns[env] = struct{}{}

	// Populate identifier-based indexes for status endpoint lookups
	e.addToIdentifierIndexes(env)
}

// MapRequestParams creates a mapping from connection parameters to an environment connection. It can be used
// if a new credential/filter is introduced which wasn't present when the environment was originally
// inserted using InsertEnvironment.
func (e *EnvironmentLookup) MapRequestParams(params credential.SDKCredential, env relayenv.EnvContext) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.mapParams(params, env)
	e.conns[env] = struct{}{}
}

// UnmapRequestParams removes a mapping from connection parameters to an environment.
func (e *EnvironmentLookup) UnmapRequestParams(params credential.SDKCredential) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.unmapParams(params)
}

// Lookup searches for a mapping from connection parameters to a suitable environment connection.
// If a connection is found, returns true; otherwise, returns false and the first value is undefined.
func (e *EnvironmentLookup) Lookup(params credential.SDKCredential) (relayenv.EnvContext, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.lookup(params)
}

// LookupByIdentifier searches for an environment by a flexible identifier string and optional filter key.
// The identifier can be:
// - An environment ID (e.g., "507f1f77bcf86cd799439011")
// - A project/environment key pair separated by "/" (e.g., "my-app/production")
// - A configured name (e.g., "My Production Environment")
//
// Lookup precedence: envID → projKey/envKey (contains "/") → configuredName
//
// If a matching environment is found, returns true; otherwise, returns false.
func (e *EnvironmentLookup) LookupByIdentifier(identifier string) (relayenv.EnvContext, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Try environment ID first (most specific)
	if envID := config.EnvironmentID(identifier); envID != "" {
		if env, ok := e.envIDIndex[envID]; ok {
			return env, true
		}
	}

	// Try project/environment key pair (contains "/")
	if idx := strings.Index(identifier, "/"); idx > 0 && idx < len(identifier)-1 {
		projKey := identifier[:idx]
		envKey := identifier[idx+1:]
		key := projEnvKey{projKey: projKey, envKey: envKey}
		if env, ok := e.projEnvKeyIndex[key]; ok {
			return env, true
		}
	}

	// Try configured name last (fallback)
	if env, ok := e.configNameIndex[identifier]; ok {
		return env, true
	}

	return nil, false
}

// DeleteEnvironment searches for an environment identified by the client request params, deletes it, and then
// removes all other credential mappings.
// If an environment was deleted, returns true; otherwise, returns false and the first value is undefined.
func (e *EnvironmentLookup) DeleteEnvironment(params credential.SDKCredential) (relayenv.EnvContext, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	env, ok := e.lookup(params)
	if ok {
		e.deleteEnvironment(env)
		return env, true
	}

	return nil, false
}

// Environments returns a list of all managed environment connections. Environments are only
// removed by DeleteEnvironment/DeleteEnvironment; removing credential mappings do not affect
// the environment itself.
func (e *EnvironmentLookup) Environments() (envs []relayenv.EnvContext) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for k := range e.conns {
		envs = append(envs, k)
	}

	return
}

func (e *EnvironmentLookup) mapParams(key credential.SDKCredential, env relayenv.EnvContext) {
	e.mapping[key] = env
}

func (e *EnvironmentLookup) unmapParams(key credential.SDKCredential) {
	delete(e.mapping, key)
}

func (e *EnvironmentLookup) lookup(key credential.SDKCredential) (relayenv.EnvContext, bool) {
	env, ok := e.mapping[key]
	return env, ok
}

func (e *EnvironmentLookup) deleteEnvironment(env relayenv.EnvContext) (found bool) {
	for k, v := range e.mapping {
		if v == env {
			e.unmapParams(k)
			found = true
		}
	}
	delete(e.conns, env)
	e.removeFromIdentifierIndexes(env)
	return
}

// addToIdentifierIndexes adds the environment to all applicable identifier-based indexes.
// This must be called with the mutex already locked.
func (e *EnvironmentLookup) addToIdentifierIndexes(env relayenv.EnvContext) {
	identifiers := env.GetIdentifiers()

	// Index by environment ID if present (auto-config mode)
	var envID config.EnvironmentID
	for _, cred := range env.GetCredentials() {
		if id, ok := cred.(config.EnvironmentID); ok && id.Defined() {
			envID = id
			break
		}
	}
	if envID != "" {
		e.envIDIndex[envID] = env
	}

	// Index by configured name if present (manual config mode)
	if identifiers.ConfiguredName != "" {
		e.configNameIndex[identifiers.ConfiguredName] = env
	}

	// Index by project+environment keys if both present (auto-config mode)
	if identifiers.ProjKey != "" && identifiers.EnvKey != "" {
		key := projEnvKey{projKey: identifiers.ProjKey, envKey: identifiers.EnvKey}
		e.projEnvKeyIndex[key] = env
	}
}

// removeFromIdentifierIndexes removes the environment from all identifier-based indexes.
// This must be called with the mutex already locked.
func (e *EnvironmentLookup) removeFromIdentifierIndexes(env relayenv.EnvContext) {
	identifiers := env.GetIdentifiers()

	// Remove from environment ID index
	var envID config.EnvironmentID
	for _, cred := range env.GetCredentials() {
		if id, ok := cred.(config.EnvironmentID); ok && id.Defined() {
			envID = id
			break
		}
	}
	if envID != "" {
		delete(e.envIDIndex, envID)
	}

	// Remove from configured name index
	if identifiers.ConfiguredName != "" {
		delete(e.configNameIndex, identifiers.ConfiguredName)
	}

	// Remove from project+environment key index
	if identifiers.ProjKey != "" && identifiers.EnvKey != "" {
		key := projEnvKey{projKey: identifiers.ProjKey, envKey: identifiers.EnvKey}
		delete(e.projEnvKeyIndex, key)
	}
}

// RefreshEnvironmentIndexes updates the identifier-based indexes for an environment.
// This should be called after the environment's identifiers have changed via SetIdentifiers.
// It removes all existing index entries for this environment and re-adds them with current identifiers.
func (e *EnvironmentLookup) RefreshEnvironmentIndexes(env relayenv.EnvContext) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Remove every entry that points at this environment instance. The identifiers have already
	// changed, so the old keys cannot be derived from the environment itself any more.
	for envID, indexed := range e.envIDIndex {
		if indexed == env {
			delete(e.envIDIndex, envID)
		}
	}

	for name, indexed := range e.configNameIndex {
		if indexed == env {
			delete(e.configNameIndex, name)
		}
	}

	for key, indexed := range e.projEnvKeyIndex {
		if indexed == env {
			delete(e.projEnvKeyIndex, key)
		}
	}

	// Re-add using current identifiers
	e.addToIdentifierIndexes(env)
}
