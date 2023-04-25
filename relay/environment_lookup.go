package relay

import (
	"sync"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
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
type EnvironmentLookup struct {
	// mapping maps {credential, filter} keys to environment connections.
	mapping map[sdkauth.ScopedCredential]relayenv.EnvContext
	// conns is the set of unique environment connections
	conns map[relayenv.EnvContext]struct{}
	// mu protects any access to 'mapping' and 'conns'
	mu sync.RWMutex
}

// NewEnvironmentLookup instantiates an empty instance of EnvironmentLookup. Calls into EnvironmentLookup
// are thread safe.
func NewEnvironmentLookup() *EnvironmentLookup {
	return &EnvironmentLookup{
		mapping: make(map[sdkauth.ScopedCredential]relayenv.EnvContext),
		conns:   make(map[relayenv.EnvContext]struct{}),
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
			e.mapParams(sdkauth.NewScoped(env.GetPayloadFilter(), cred), env)
		}
	}

	e.conns[env] = struct{}{}
}

// MapRequestParams creates a mapping from connection parameters to an environment connection. It can be used
// if a new credential/filter is introduced which wasn't present when the environment was originally
// inserted using InsertEnvironment.
func (e *EnvironmentLookup) MapRequestParams(params sdkauth.ScopedCredential, env relayenv.EnvContext) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.mapParams(params, env)
	e.conns[env] = struct{}{}
}

// UnmapRequestParams removes a mapping from connection parameters to an environment.
func (e *EnvironmentLookup) UnmapRequestParams(params sdkauth.ScopedCredential) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.unmapParams(params)
}

// Lookup searches for a mapping from connection parameters to a suitable environment connection.
// If a connection is found, returns true; otherwise, returns false and the first value is undefined.
func (e *EnvironmentLookup) Lookup(params sdkauth.ScopedCredential) (relayenv.EnvContext, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.lookup(params)
}

// DeleteEnvironment searches for an environment identified by the client request params, deletes it, and then
// removes all other credential mappings.
// If an environment was deleted, returns true; otherwise, returns false and the first value is undefined.
func (e *EnvironmentLookup) DeleteEnvironment(params sdkauth.ScopedCredential) (relayenv.EnvContext, bool) {
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

func (e *EnvironmentLookup) mapParams(key sdkauth.ScopedCredential, env relayenv.EnvContext) {
	e.mapping[key] = env
}

func (e *EnvironmentLookup) unmapParams(key sdkauth.ScopedCredential) {
	delete(e.mapping, key)
}

func (e *EnvironmentLookup) lookup(key sdkauth.ScopedCredential) (relayenv.EnvContext, bool) {
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
	return
}
