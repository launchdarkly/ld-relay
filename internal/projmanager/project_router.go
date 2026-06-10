package projmanager

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/autoconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

var _ autoconfig.MessageHandler = &ProjectRouter{}

// AutoConfigActions represents all possible concrete actions that may occur based on autoconfig messages.
type AutoConfigActions interface {
	EnvironmentActions
	ReceivedAllEnvironments()
}

// ProjectRouter is responsible for accepting commands relating to the creation, destruction, or modification of
// environments and filters, and then forwarding them to a ProjectManager based on the environment/filter's project key.
type ProjectRouter struct {
	managers map[string]*EnvironmentManager
	actions  AutoConfigActions
	loggers  ldlog.Loggers
}

func (e *ProjectRouter) Manager(projKey string) *EnvironmentManager {
	return e.managers[projKey]
}

func (e *ProjectRouter) Projects() []string {
	projects := make([]string, 0, len(e.managers))
	for proj := range e.managers {
		projects = append(projects, proj)
	}
	return projects
}

// NewProjectRouter creates a new router which is ready to accept commands.
func NewProjectRouter(handler AutoConfigActions, loggers ldlog.Loggers) *ProjectRouter {
	loggers.SetPrefix("[ProjectRouter]")
	return &ProjectRouter{managers: make(map[string]*EnvironmentManager), actions: handler, loggers: loggers}
}

// AddEnvironment routes the given EnvironmentParams to the relevant ProjectManager based on its project key, or instantiates
// a new ProjectManager if one doesn't already exist.
func (e *ProjectRouter) AddEnvironment(params envfactory.EnvironmentParams) {
	proj := params.Identifiers.ProjKey
	manager, ok := e.managers[proj]
	if !ok {
		e.managers[proj] = NewEnvironmentManager(proj, e.actions, e.loggers)
		manager = e.managers[proj]
	}
	manager.AddEnvironment(params)
}

// UpdateEnvironment routes the given EnvironmentParams to the relevant ProjectManager based on its project key.
// If no such manager exists, the params are ignored and an error is logged.
func (e *ProjectRouter) UpdateEnvironment(params envfactory.EnvironmentParams) {
	proj := params.Identifiers.ProjKey
	manager, ok := e.managers[proj]
	if ok {
		manager.UpdateEnvironment(params)
	} else {
		e.loggers.Errorf("precondition violation: received updated config for (%s), but environment was never added", params.Identifiers.GetDisplayName())
	}
}

// DeleteEnvironment dispatches a deletion command for the given environment ID to all ProjectManagers. It is
// assumed that environment IDs are unique, and therefore only one manager will service the request.
func (e *ProjectRouter) DeleteEnvironment(id config.EnvironmentID) {
	deleteCount := 0
	for _, manager := range e.managers {
		if manager.DeleteEnvironment(id) {
			deleteCount++
		}
	}
	if deleteCount == 0 {
		e.loggers.Errorf("precondition violation: received delete request for environment (%s), but it is not under management", id)
	} else if deleteCount > 1 {
		e.loggers.Errorf("precondition violation: received delete request for environment (%s), which was associated with more than one project", id)
	}
}

// ReceivedAllEnvironments directly invokes the underlying AutoConfigAction's ReceivedAllEnvironments method.
func (e *ProjectRouter) ReceivedAllEnvironments() {
	e.actions.ReceivedAllEnvironments()
}

// AddFilter routes the given FilterRep to the relevant ProjectManager based on its project key, or instantiates
// a new ProjectManager if one doesn't already exist.
func (e *ProjectRouter) AddFilter(params envfactory.FilterParams) {
	proj := params.ProjKey
	manager, ok := e.managers[proj]
	if !ok {
		e.managers[proj] = NewEnvironmentManager(proj, e.actions, e.loggers)
		manager = e.managers[proj]
	}
	manager.AddFilter(params)
}

// DeleteFilter dispatches a deletion command for the given filter ID to all ProjectManagers. It is
// assumed that filter IDs are unique, and therefore only one manager will service the request.
func (e *ProjectRouter) DeleteFilter(id config.FilterID) {
	deleteCount := 0
	for _, manager := range e.managers {
		if manager.DeleteFilter(id) {
			deleteCount++
		}
	}
	if deleteCount == 0 {
		e.loggers.Errorf("precondition violation: received delete request for filter (%s), but it is not under management", id)
	} else if deleteCount > 1 {
		e.loggers.Errorf("precondition violation: received delete request for filter (%s), which was associated with more than one project", id)
	}
}
