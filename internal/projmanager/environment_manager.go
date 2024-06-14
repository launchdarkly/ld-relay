package projmanager

import (
	"fmt"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

type EnvironmentActions interface {
	AddEnvironment(params envfactory.EnvironmentParams)
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
	defaults map[config.EnvironmentID]envfactory.EnvironmentParams
	filtered map[config.FilterID]*filterMapping
	project  string
	loggers  ldlog.Loggers
	handler  EnvironmentActions
}

func NewEnvironmentManager(project string, handler EnvironmentActions, loggers ldlog.Loggers) *EnvironmentManager {
	loggers.SetPrefix(fmt.Sprintf("[EnvironmentManager(%s)]", project))

	return &EnvironmentManager{
		project:  project,
		defaults: make(map[config.EnvironmentID]envfactory.EnvironmentParams),
		filtered: make(map[config.FilterID]*filterMapping),
		loggers:  loggers,
		handler:  handler,
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

	// The new environment is considered "default" - meaning unfiltered.
	e.defaults[env.EnvID] = env
	// This is where logic would go to suppress creation of a default environment, if such a configuration
	// was desirable.
	e.handler.AddEnvironment(env)

	for _, filter := range e.filtered {
		// Associate the new environment with all existing filters, and..
		filter.envs[env.EnvID] = struct{}{}
		// Spawn a new filtered environment.
		e.handler.AddEnvironment(env.WithFilter(filter.key))
	}
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
		e.handler.AddEnvironment(env.WithFilter(filter.Key))
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
