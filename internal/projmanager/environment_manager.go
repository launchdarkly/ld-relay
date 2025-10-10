package projmanager

import (
	"fmt"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/cache"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

type EnvironmentActions interface {
	AddEnvironment(params envfactory.EnvironmentParams, sharedCache *cache.SharedObjectCache)
	UpdateEnvironment(params envfactory.EnvironmentParams)
	DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey)
}

type filterMapping struct {
	key  config.FilterKey
	envs map[config.EnvironmentID]struct{}
}

// An EnvironmentManager manages the opening, modification, and closing of connections to LaunchDarkly environments
// for a particular LaunchDarkly project.
//
// Assume there are M projects, each of which has N environments and K filters configured. Then:
// - M EnvironmentManagers must be instantiated
// - Within a given EnvironmentManager, N "default" environments must be setup
// - Additionally, N*K "filtered environments" must be setup
// In total, each EnvironmentManager would then manage N*(K+1) environments.
type EnvironmentManager struct {
	defaults          map[config.EnvironmentID]envfactory.EnvironmentParams
	filtered          map[config.FilterID]*filterMapping
	project           string
	loggers           ldlog.Loggers
	handler           EnvironmentActions
	objectCacheConfig config.ObjectCacheConfig
	sharedCaches      map[string]*cache.SharedObjectCache // Allows sharing between filter and unfiltered envs by indexing on env ID.
}

func NewEnvironmentManager(project string, handler EnvironmentActions, objectCacheConfig config.ObjectCacheConfig, loggers ldlog.Loggers) *EnvironmentManager {
	loggers.SetPrefix(fmt.Sprintf("[EnvironmentManager(%s)]", project))

	return &EnvironmentManager{
		project:           project,
		defaults:          make(map[config.EnvironmentID]envfactory.EnvironmentParams),
		filtered:          make(map[config.FilterID]*filterMapping),
		loggers:           loggers,
		handler:           handler,
		objectCacheConfig: objectCacheConfig,
		sharedCaches:      make(map[string]*cache.SharedObjectCache),
	}
}

func (e *EnvironmentManager) UpdateEnvironment(env envfactory.EnvironmentParams) {
	_, ok := e.defaults[env.EnvID]
	if !ok {
		return
	}

	e.handler.UpdateEnvironment(env)
	for _, filter := range e.filtered {
		e.handler.UpdateEnvironment(env.WithFilter(filter.key))
	}
}

func (e *EnvironmentManager) AddEnvironment(env envfactory.EnvironmentParams) {
	_, ok := e.defaults[env.EnvID]
	if ok {
		return
	}

	sharedCache := e.getOrCreateSharedCache(string(env.EnvID), &e.objectCacheConfig)

	// The new environment is considered "default" - meaning unfiltered.
	e.defaults[env.EnvID] = env
	// This is where logic would go to suppress creation of a default environment, if such a configuration
	// was desirable.
	e.handler.AddEnvironment(env, sharedCache)

	for _, filter := range e.filtered {
		// Associate the new environment with all existing filters, and..
		filter.envs[env.EnvID] = struct{}{}
		// Spawn a new filtered environment.
		e.handler.AddEnvironment(env.WithFilter(filter.key), sharedCache)
	}
}

// getOrCreateSharedCache returns an existing shared cache for the base environment
// or creates a new one if it doesn't exist
func (e *EnvironmentManager) getOrCreateSharedCache(baseEnvKey string, objectCacheConfig *config.ObjectCacheConfig) *cache.SharedObjectCache {
	if existingCache, exists := e.sharedCaches[baseEnvKey]; exists {
		return existingCache
	}

	// Create new shared cache for this base environment
	cacheConfig := cache.NewCacheConfigFromRelay(*objectCacheConfig)
	sharedCache := cache.NewSharedObjectCache(cacheConfig, e.loggers)
	e.sharedCaches[baseEnvKey] = sharedCache

	e.loggers.Debugf("Created shared object cache for base environment %s - enabled: %t, max objects: %d, TTL: %v",
		baseEnvKey, cacheConfig.Enabled, cacheConfig.MaxObjects, cacheConfig.TTL)

	// Start the cleanup routine for this cache
	// It will be stopped when DeleteEnvironment calls StopCleanupRoutine
	sharedCache.StartCleanupRoutine()

	return sharedCache
}

func (e *EnvironmentManager) DeleteEnvironment(id config.EnvironmentID) bool {
	_, ok := e.defaults[id]

	if !ok {
		return false
	}

	delete(e.defaults, id)

	e.handler.DeleteEnvironment(id, config.DefaultFilter)

	for _, filter := range e.filtered {
		delete(filter.envs, id)
		e.handler.DeleteEnvironment(id, filter.key)
	}

	if cache, exists := e.sharedCaches[string(id)]; exists {
		cache.StopCleanupRoutine()
	}
	delete(e.sharedCaches, string(id))

	return true
}

func (e *EnvironmentManager) AddFilter(filter envfactory.FilterParams) {
	_, ok := e.filtered[filter.ID]
	if ok {
		return
	}

	mapping := &filterMapping{
		key:  filter.Key,
		envs: make(map[config.EnvironmentID]struct{}, len(e.defaults)),
	}

	for id, env := range e.defaults {
		mapping.envs[id] = struct{}{}
		sharedObjectCache := e.sharedCaches[string(id)]
		e.handler.AddEnvironment(env.WithFilter(filter.Key), sharedObjectCache)
	}

	e.filtered[filter.ID] = mapping
}

func (e *EnvironmentManager) DeleteFilter(filter config.FilterID) bool {
	filtered, ok := e.filtered[filter]
	if !ok {
		return false
	}

	for id := range filtered.envs {
		e.handler.DeleteEnvironment(id, filtered.key)
	}

	delete(e.filtered, filter)
	return true
}

func (e *EnvironmentManager) Filters() []config.FilterKey {
	filters := make([]config.FilterKey, 0, len(e.filtered))
	for _, filter := range e.filtered {
		filters = append(filters, filter.key)
	}
	return filters
}

func (e *EnvironmentManager) Environments() []config.EnvironmentID {
	envs := make([]config.EnvironmentID, 0, len(e.defaults))
	for id := range e.defaults {
		envs = append(envs, id)
	}
	for _, m := range e.filtered {
		for id := range m.envs {
			envs = append(envs, config.EnvironmentID(fmt.Sprintf("%s/%s", id, m.key)))
		}
	}
	return envs
}
