package projmanager

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/stretchr/testify/require"
)

func makeEnv(id string, proj string) envfactory.EnvironmentParams {
	return envfactory.EnvironmentParams{EnvID: config.EnvironmentID(id), Identifiers: relayenv.EnvIdentifiers{ProjKey: proj}}
}

func makeFilter(key string, proj string) envfactory.FilterParams {
	return envfactory.FilterParams{
		ProjKey: proj,
		Key:     config.FilterKey(key),
		ID:      config.FilterID(fmt.Sprintf("%s.%s", proj, key)),
	}
}

// Makes a distribution of EnvironmentParams, each randomly assigned to a project.
func makeEnvs(count int, projects []string) []envfactory.EnvironmentParams {
	var envs []envfactory.EnvironmentParams
	for i := 0; i < count; i++ {
		proj := projects[rand.Intn(len(projects))]
		envs = append(envs, makeEnv(fmt.Sprintf("env-%v", i), proj))
	}
	return envs
}

// Makes a distribution of FilterReps, each randomly assigned to a project.
func makeFilters(count int, projects []string) []envfactory.FilterParams {
	var filters []envfactory.FilterParams
	for i := 0; i < count; i++ {
		proj := projects[rand.Intn(len(projects))]
		filters = append(filters, makeFilter(fmt.Sprintf("filter-%v", i), proj))
	}
	return filters
}

type noopActions struct {
	spyHandler
}

func (n *spyHandler) ReceivedAllEnvironments() {

}

func TestProjectRouter_NewIsEmpty(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	router := NewProjectRouter(&noopActions{}, mockLog.Loggers)
	require.Empty(t, router.Projects())
}

// The purpose of this test is to find a combination of environments/filters/projects that violates either of two properties:
//  1. The number of projects currently managed by the router never exceeds the amount of unique projects seen by the router
//     (either via adding environments, or filters.)
//  2. Every project currently managed by the router was a project that was seen by the router
//     (either via adding environments, or filters.)
func TestProjectRouter_VerifySetProperty(t *testing.T) {

	// Makes a list of unique project keys.
	makeProjects := func(count int) []string {
		var projects []string
		for i := 0; i < count; i++ {
			projects = append(projects, fmt.Sprintf("proj-%v", i))
		}
		return projects
	}

	// Converts a list of project keys into a set for quick lookup.
	makeProjectMap := func(projects []string) map[string]struct{} {
		projectsMap := make(map[string]struct{})
		for _, p := range projects {
			projectsMap[p] = struct{}{}
		}
		return projectsMap
	}

	// Returns false if the properties under test fail. For debugging purposes, add log statements
	// when a property fails to make debugging easier.
	checkProperty := func(t *testing.T, nEnvironments, nProjects, nFilters int) bool {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		router := NewProjectRouter(newHandlerSpy(), mockLog.Loggers)

		expectedProjects := makeProjects(nProjects)

		envs := makeEnvs(nEnvironments, expectedProjects)
		filters := makeFilters(nFilters, expectedProjects)

		for _, e := range envs {
			router.AddEnvironment(e)
		}

		for _, f := range filters {
			router.AddFilter(f)
		}

		gotProjects := router.Projects()

		if len(gotProjects) > len(expectedProjects) {
			t.Errorf("number of projects (%v) exceeded number of expected projects (%v)", len(gotProjects), len(expectedProjects))
			return false
		}

		expectedProjectsMap := makeProjectMap(expectedProjects)

		for _, g := range gotProjects {
			if _, ok := expectedProjectsMap[g]; !ok {
				t.Errorf("project (%s) seen by router was not in expected list", g)
				return false
			}

		}
		return true
	}

	// This test verifies the properties exhaustively up to a reasonable (somewhat arbitrary) limit
	// of environments, projects, and filters. Since this is a triple-for-loop, test runtime could
	// quickly grow out of control so modify carefully.
	t.Run("exhaustive tests", func(t *testing.T) {
		maxEnvironments := 16
		maxProjects := 16
		maxFilters := 32

		for nEnvironments := 0; nEnvironments < maxEnvironments; nEnvironments++ {
			// nProjects starts at 1 because every environment has a project assigned to it; we need something
			// to choose from.
			for nProjects := 1; nProjects < maxProjects; nProjects++ {
				for nFilters := 0; nFilters < maxFilters; nFilters++ {
					if !checkProperty(t, nEnvironments, nProjects, nFilters) {
						t.Fatalf("failed for (%v) projects, (%v) environments, (%v) filters", nProjects, nEnvironments, nFilters)
					}
				}
			}
		}
	})

	// Since it's impractical to exhaustively verify the property with large amounts of environments/projects,
	// cherry-pick a few scenarios.
	t.Run("spot checks", func(t *testing.T) {

		type scenario struct {
			environments int
			projects     int
			filters      int
		}

		scenarios := []scenario{
			{
				environments: 1000,
				projects:     10,
				filters:      10,
			},
			{
				environments: 10,
				projects:     1000,
				filters:      10,
			},
			{
				environments: 10,
				projects:     10,
				filters:      1000,
			},
			{
				environments: 1000,
				projects:     1000,
				filters:      1000,
			},
		}

		for _, scenario := range scenarios {
			t.Run(fmt.Sprintf("%v environments %v projects %v filters", scenario.environments, scenario.projects, scenario.filters), func(t *testing.T) {
				if !checkProperty(t, scenario.environments, scenario.projects, scenario.filters) {
					t.Fail()
				}
			})
		}
	})
}
