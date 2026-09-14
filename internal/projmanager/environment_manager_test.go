package projmanager

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"
)

type deleteParams struct {
	id config.EnvironmentID
}

// Convenience function to use when comparing a list of environments with deletions recorded by a spy,
// example:
//
//	envs := makeEnvs(10, []string{"foo"})
//	<do stuff with envs>
//	require.ElementsMatch(t, spy.deleted, makeDeletions(envs))
func makeDeletions(envs []envfactory.EnvironmentParams) (params []deleteParams) {
	for _, e := range envs {
		params = append(params, deleteParams{e.EnvID})
	}
	return
}

type spyHandler struct {
	added   []envfactory.EnvironmentParams
	updated []envfactory.EnvironmentParams
	deleted []deleteParams
}

func newHandlerSpy() *spyHandler {
	return &spyHandler{
		added:   make([]envfactory.EnvironmentParams, 0),
		updated: make([]envfactory.EnvironmentParams, 0),
		deleted: make([]deleteParams, 0),
	}
}

func (n *spyHandler) AddEnvironment(params envfactory.EnvironmentParams) {
	n.added = append(n.added, params)
}

func (n *spyHandler) UpdateEnvironment(params envfactory.EnvironmentParams) {
	n.updated = append(n.updated, params)
}

func (n *spyHandler) DeleteEnvironment(id config.EnvironmentID) {
	n.deleted = append(n.deleted, deleteParams{id})
}

func TestEnvManager_NewManagerIsEmpty(t *testing.T) {
	mockLog, _ := logtest.NewMockLogger()

	m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog)

	require.Len(t, m.Environments(), 0, "new manager should not be managing any environments")
}

func TestEnvironmentManager_AddEnvironment(t *testing.T) {
	t.Run("adding environments is reflected in state", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog)

		in := []envfactory.EnvironmentParams{
			{EnvID: config.EnvironmentID("a")},
			{EnvID: config.EnvironmentID("b")},
		}

		for _, e := range in {
			m.AddEnvironment(e)
		}

		out := m.Environments()
		require.Len(t, out, len(in), "environments should be added")
		require.ElementsMatchf(t, out, []config.EnvironmentID{"a", "b"}, "environments should match")
	})

	t.Run("no duplicate environments", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog)

		e := makeEnv("env", "foo")

		for i := 0; i < 10; i++ {
			m.AddEnvironment(e)
		}

		out := m.Environments()
		require.Len(t, out, 1, "single environment should be added")
		require.ElementsMatchf(t, out, []config.EnvironmentID{"env"}, "environment should match")
	})

	t.Run("adds n environments", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog)

			envs := makeEnvs(i, []string{"foo"})
			for _, e := range envs {
				m.AddEnvironment(e)
			}
			require.ElementsMatch(t, spy.added, envs)
		}
	})
}

func TestEnvironmentManager_DeleteEnvironment(t *testing.T) {
	t.Run("environment state is correct", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog)

		in := []envfactory.EnvironmentParams{
			{EnvID: config.EnvironmentID("a")},
			{EnvID: config.EnvironmentID("b")},
		}

		for _, e := range in {
			m.AddEnvironment(e)
		}

		for _, e := range in {
			m.DeleteEnvironment(e.EnvID)
		}

		require.Len(t, m.Environments(), 0, "all environments should be deleted")
	})

	t.Run("deletes n environments", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog)

			envs := makeEnvs(i, []string{"foo"})
			for _, e := range envs {
				m.AddEnvironment(e)
			}
			for _, e := range envs {
				m.DeleteEnvironment(e.EnvID)
			}
			require.ElementsMatch(t, spy.deleted, makeDeletions(envs))
		}
	})

	t.Run("delete unknown environment has no effect", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		spy := newHandlerSpy()
		m := NewEnvironmentManager("foo", spy, mockLog)
		m.AddEnvironment(makeEnv("known", "foo"))
		m.DeleteEnvironment("unknown")
		require.Len(t, spy.deleted, 0)
	})
}

func TestEnvironmentManager_UpdateEnvironment(t *testing.T) {
	t.Run("updating should not modify environment state", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog)

		envs := makeEnvs(2, []string{"foo"})
		for _, e := range envs {
			m.AddEnvironment(e)
		}

		for i := 0; i < 3; i++ {
			for _, e := range envs {
				m.UpdateEnvironment(e)
			}
		}

		require.Len(t, m.Environments(), len(envs), "same amount of environments should exist")
	})

	t.Run("update unknown environment has no effect", func(t *testing.T) {
		mockLog, _ := logtest.NewMockLogger()

		spy := newHandlerSpy()
		m := NewEnvironmentManager("foo", spy, mockLog)
		m.AddEnvironment(makeEnv("known", "proj"))
		m.UpdateEnvironment(makeEnv("unknown", "proj"))
		require.Len(t, spy.updated, 0)
	})

}

type command struct {
	op           commandType
	value        string
	expectedEnvs []config.EnvironmentID
}

func (c command) parseVals() []string {
	out := strings.Split(c.value, ",")
	for i, e := range out {
		out[i] = strings.TrimSpace(e)
	}
	return out
}

type commandType string

const (
	addEnvironments    = commandType("add environments")
	deleteEnvironments = commandType("delete environments")
)

type scenario struct {
	name     string
	commands []command
}

func TestEnvironmentManager_TableDriven(t *testing.T) {
	mockLog, _ := logtest.NewMockLogger()

	scenarios := []scenario{
		{
			name: "adding and removing environments",
			commands: []command{
				{
					op:           addEnvironments,
					value:        "a,b,c",
					expectedEnvs: []config.EnvironmentID{"a", "b", "c"},
				},
				{
					op:           deleteEnvironments,
					value:        "c",
					expectedEnvs: []config.EnvironmentID{"a", "b"},
				},
				{
					op:           addEnvironments,
					value:        "d,e,f",
					expectedEnvs: []config.EnvironmentID{"a", "b", "d", "e", "f"},
				},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog)
			for _, cmd := range scenario.commands {
				t.Logf("%s (%v), expecting envs (%v)", cmd.op, cmd.value, cmd.expectedEnvs)
				vals := cmd.parseVals()
				switch cmd.op {
				case addEnvironments:
					for _, env := range vals {
						m.AddEnvironment(envfactory.EnvironmentParams{EnvID: config.EnvironmentID(env)})
					}
				case deleteEnvironments:
					for _, env := range vals {
						m.DeleteEnvironment(config.EnvironmentID(env))
					}
				}
				require.ElementsMatch(t, m.Environments(), cmd.expectedEnvs)
			}
		})
	}
}
