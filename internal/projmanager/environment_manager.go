package projmanager

import (
	"fmt"
	"log/slog"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
)

type EnvironmentActions interface {
	AddEnvironment(params envfactory.EnvironmentParams)
	UpdateEnvironment(params envfactory.EnvironmentParams)
	DeleteEnvironment(id config.EnvironmentID)
}

// An EnvironmentManager manages the opening, modification, and closing of connections to LaunchDarkly environments
// for a particular LaunchDarkly project.
//
// One EnvironmentManager is instantiated per project, and it manages that project's environments.
type EnvironmentManager struct {
	defaults map[config.EnvironmentID]envfactory.EnvironmentParams
	project  string
	logger   *slog.Logger
	handler  EnvironmentActions
}

func NewEnvironmentManager(project string, handler EnvironmentActions, logger *slog.Logger) *EnvironmentManager {
	logger = logger.With("component", fmt.Sprintf("EnvironmentManager(%s)", project))

	return &EnvironmentManager{
		project:  project,
		defaults: make(map[config.EnvironmentID]envfactory.EnvironmentParams),
		logger:   logger,
		handler:  handler,
	}
}

func (e *EnvironmentManager) UpdateEnvironment(env envfactory.EnvironmentParams) {
	_, ok := e.defaults[env.EnvID]
	if !ok {
		return
	}

	e.defaults[env.EnvID] = env

	e.handler.UpdateEnvironment(env)
}

func (e *EnvironmentManager) AddEnvironment(env envfactory.EnvironmentParams) {
	_, ok := e.defaults[env.EnvID]
	if ok {
		return
	}

	e.defaults[env.EnvID] = env
	e.handler.AddEnvironment(env)
}

func (e *EnvironmentManager) DeleteEnvironment(id config.EnvironmentID) bool {
	_, ok := e.defaults[id]

	if !ok {
		return false
	}

	delete(e.defaults, id)

	e.handler.DeleteEnvironment(id)

	return true
}

func (e *EnvironmentManager) Environments() []config.EnvironmentID {
	envs := make([]config.EnvironmentID, 0, len(e.defaults))
	for id := range e.defaults {
		envs = append(envs, id)
	}
	return envs
}
