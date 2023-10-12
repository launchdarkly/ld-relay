package projmanager

import (
	"strings"
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/stretchr/testify/require"
)

type deleteParams struct {
	id     config.EnvironmentID
	filter config.FilterKey
}

// Convenience function to use when comparing a list of environments with deletions recorded by a spy,
// example:
//
//	envs := makeEnvs(10, []string{"foo"})
//	<do stuff with envs>
//	require.ElementsMatch(t, spy.deleted, makeDeletions(envs))
func makeDeletions(envs []envfactory.EnvironmentParams) (params []deleteParams) {
	for _, e := range envs {
		params = append(params, deleteParams{e.EnvID, e.Identifiers.FilterKey})
	}
	return
}

type expiredParams struct {
	id     config.EnvironmentID
	filter config.FilterKey
	key    config.SDKKey
}
type spyHandler struct {
	added   []envfactory.EnvironmentParams
	updated []envfactory.EnvironmentParams
	deleted []deleteParams
	expired []expiredParams
}

func newHandlerSpy() *spyHandler {
	return &spyHandler{
		added:   make([]envfactory.EnvironmentParams, 0),
		updated: make([]envfactory.EnvironmentParams, 0),
		deleted: make([]deleteParams, 0),
		expired: make([]expiredParams, 0),
	}
}

func (n *spyHandler) AddEnvironment(params envfactory.EnvironmentParams) {
	n.added = append(n.added, params)
}

func (n *spyHandler) UpdateEnvironment(params envfactory.EnvironmentParams) {
	n.updated = append(n.updated, params)
}

func (n *spyHandler) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	n.deleted = append(n.deleted, deleteParams{id, filter})
}

func (n *spyHandler) KeyExpired(id config.EnvironmentID, filter config.FilterKey, key config.SDKKey) {
	n.expired = append(n.expired, expiredParams{id, filter, key})
}

func TestEnvManager_NewManagerIsEmpty(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

	require.Len(t, m.Filters(), 0, "new manager should not be managing any filters")
	require.Len(t, m.Environments(), 0, "new manager should not be managing any environments")
}

func TestEnvManager_AddFilters(t *testing.T) {

	t.Run("adding filters is reflected in state", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

		keys := []config.FilterKey{"a", "b"}

		for _, f := range keys {
			m.AddFilter(makeFilter(string(f), "proj"))
		}

		out := m.Filters()
		require.Len(t, out, len(keys), "all filters should be added")
		require.ElementsMatchf(t, out, keys, "filters in should equal filters out")
	})

	t.Run("no duplicates are allowed", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

		for i := 0; i < 10; i++ {
			m.AddFilter(makeFilter("a", "proj"))
		}

		filters := m.Filters()
		require.Len(t, filters, 1, "single filter should be added")
		require.ElementsMatchf(t, filters, []config.FilterKey{"a"}, "filter should match")
	})

	t.Run("adds a new environment for each existing environment", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog.Loggers)

			envs := makeEnvs(i, []string{"foo"})

			filter := makeFilter("filter", "foo")
			var expectedEnvs []envfactory.EnvironmentParams
			for _, e := range envs {
				expectedEnvs = append(expectedEnvs, e, e.WithFilter(filter.Key))
				m.AddEnvironment(e)
			}

			m.AddFilter(filter)

			require.ElementsMatch(t, spy.added, expectedEnvs)
		}
	})

}
func TestEnvManager_DeleteFilters(t *testing.T) {
	t.Run("after adding and removing same filters, state reflects no filters", func(t *testing.T) {

		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

		in := []string{"a", "b"}
		for _, f := range in {
			m.AddFilter(makeFilter(f, "proj"))
		}

		out := m.Filters()
		require.Len(t, out, len(in))

		for _, f := range in {
			m.DeleteFilter(makeFilter(f, "proj").ID)
		}

		out = m.Filters()
		require.Len(t, out, 0, "no filters should be managed")
	})

	t.Run("deleting filters deletes filtered environments", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog.Loggers)
			filters := makeFilters(i, []string{"foo"})
			for _, f := range filters {
				m.AddFilter(f)
			}
			env := makeEnv("env", "foo")
			m.AddEnvironment(env)

			for _, f := range filters {
				m.DeleteFilter(f.ID)
			}

			var expDeletions []deleteParams
			for _, f := range filters {
				expDeletions = append(expDeletions, deleteParams{
					id:     env.EnvID,
					filter: f.Key,
				})
			}
			require.ElementsMatch(t, spy.deleted, expDeletions)
		}
	})

	t.Run("delete unknown filter has no effect", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		spy := newHandlerSpy()
		m := NewEnvironmentManager("foo", spy, mockLog.Loggers)
		m.AddEnvironment(makeEnv("proj", "foo"))
		m.AddFilter(makeFilter("known", "foo"))
		m.DeleteFilter("unknown")
		require.Len(t, spy.deleted, 0)
	})
}

func TestEnvironmentManager_AddEnvironment(t *testing.T) {
	t.Run("adding environments is reflected in state", func(t *testing.T) {

		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

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
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

		e := makeEnv("env", "foo")

		for i := 0; i < 10; i++ {
			m.AddEnvironment(e)
		}

		out := m.Environments()
		require.Len(t, out, 1, "single environment should be added")
		require.ElementsMatchf(t, out, []config.EnvironmentID{"env"}, "environment should match")
	})

	t.Run("adds n environments", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog.Loggers)

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
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

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
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog.Loggers)

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
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		spy := newHandlerSpy()
		m := NewEnvironmentManager("foo", spy, mockLog.Loggers)
		m.AddEnvironment(makeEnv("known", "foo"))
		m.DeleteEnvironment("unknown")
		require.Len(t, spy.deleted, 0)
	})
}

func TestEnvironmentManager_UpdateEnvironment(t *testing.T) {
	t.Run("updating should not modify environment or filter state", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

		envs := makeEnvs(2, []string{"foo"})
		filters := makeFilters(2, []string{"foo"})

		for _, e := range envs {
			m.AddEnvironment(e)
		}
		for _, f := range filters {
			m.AddFilter(f)
		}

		for i := 0; i < 3; i++ {
			for _, e := range envs {
				m.UpdateEnvironment(e)
			}
		}

		require.Len(t, m.Environments(), len(envs)*(len(filters)+1), "same amount of environments should exist")
		require.Len(t, m.Filters(), len(filters), "same amount of filters should exist")
	})

	t.Run("n filters dispatches to n+1 environments", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog.Loggers)

			env := makeEnv("env1", "foo")

			m.AddEnvironment(env)

			filters := makeFilters(i, []string{"foo"})
			expectedEnvs := []envfactory.EnvironmentParams{env}

			for _, filter := range filters {
				expectedEnvs = append(expectedEnvs, env.WithFilter(filter.Key))
			}

			for _, filter := range filters {
				m.AddFilter(filter)
			}

			m.UpdateEnvironment(env)

			require.ElementsMatch(t, spy.updated, expectedEnvs)
		}
	})

	t.Run("update unknown environment has no effect", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		spy := newHandlerSpy()
		m := NewEnvironmentManager("foo", spy, mockLog.Loggers)
		m.AddEnvironment(makeEnv("known", "proj"))
		m.UpdateEnvironment(makeEnv("unknown", "proj"))
		require.Len(t, spy.updated, 0)
	})

}

func TestEnvironmentManager_SimpleFilterCombination(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)

	envs := []envfactory.EnvironmentParams{
		{
			EnvID: config.EnvironmentID("a"),
		},
		{
			EnvID: config.EnvironmentID("b"),
		},
	}

	filters := []string{"foo", "bar"}

	for _, e := range envs {
		m.AddEnvironment(e)
	}

	for _, f := range filters {
		m.AddFilter(makeFilter(f, "proj"))
	}

	out := m.Environments()
	require.Len(t, out, len(envs)*(len(filters)+1), "there should be len(envs) * (len(filters)+1) environments")
	require.ElementsMatchf(t, out, []config.EnvironmentID{"a", "b", "a/foo", "a/bar", "b/foo", "b/bar"}, "default and filtered environments should be created")
}

func TestEnvironmentManager_KeyExpired(t *testing.T) {
	t.Run("key expiry is broadcast n times", func(t *testing.T) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		mockLog.Loggers.SetMinLevel(ldlog.Debug)

		for i := 0; i < 10; i++ {
			spy := newHandlerSpy()
			m := NewEnvironmentManager("foo", spy, mockLog.Loggers)

			filters := makeFilters(i, []string{"foo"})
			expected := []expiredParams{{id: "foo", filter: config.DefaultFilter, key: "sdk-123"}}
			for _, f := range filters {
				expected = append(expected, expiredParams{
					id:     "foo",
					filter: f.Key,
					key:    "sdk-123",
				})
				m.AddFilter(f)
			}

			m.KeyExpired("foo", "sdk-123")
			require.ElementsMatch(t, spy.expired, expected)
		}
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
	addFilters         = commandType("add filters")
	deleteFilters      = commandType("delete filters")
)

type scenario struct {
	name     string
	commands []command
}

func TestEnvironmentManager_TableDriven(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	scenarios := []scenario{
		{
			name: "adding and removing environments without filters",
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
		{
			name: "adding and removing filters with single environment",
			commands: []command{
				{
					op:           addEnvironments,
					value:        "a",
					expectedEnvs: []config.EnvironmentID{"a"},
				},
				{
					op:           addFilters,
					value:        "foo,bar,baz",
					expectedEnvs: []config.EnvironmentID{"a", "a/foo", "a/bar", "a/baz"},
				},
				{
					op:           deleteFilters,
					value:        "bar",
					expectedEnvs: []config.EnvironmentID{"a", "a/foo", "a/baz"},
				},
				{
					op:           addFilters,
					value:        "quz",
					expectedEnvs: []config.EnvironmentID{"a", "a/foo", "a/baz", "a/quz"},
				},
			},
		},
		{
			name: "adding and removing environments with a single filter",
			commands: []command{
				{
					op:           addEnvironments,
					value:        "a",
					expectedEnvs: []config.EnvironmentID{"a"},
				},
				{
					op:           addFilters,
					value:        "foo",
					expectedEnvs: []config.EnvironmentID{"a", "a/foo"},
				},
				{
					op:           addEnvironments,
					value:        "b,c",
					expectedEnvs: []config.EnvironmentID{"a", "b", "c", "a/foo", "b/foo", "c/foo"},
				},
				{
					op:           deleteEnvironments,
					value:        "b",
					expectedEnvs: []config.EnvironmentID{"a", "c", "a/foo", "c/foo"},
				},
			},
		},
		{
			name: "adding an environment after adding filters",
			commands: []command{
				{
					op:           addFilters,
					value:        "foo,bar,baz",
					expectedEnvs: []config.EnvironmentID{},
				},
				{
					op:           addEnvironments,
					value:        "a",
					expectedEnvs: []config.EnvironmentID{"a", "a/foo", "a/bar", "a/baz"},
				},
			},
		},
	}

	makeFilter := func(key string) envfactory.FilterParams {
		return envfactory.FilterParams{
			ProjKey: "proj",
			ID:      config.FilterID("proj." + key),
			Key:     config.FilterKey(key),
		}
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			m := NewEnvironmentManager("foo", newHandlerSpy(), mockLog.Loggers)
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
				case addFilters:
					for _, filter := range vals {
						m.AddFilter(makeFilter(filter))
					}
				case deleteFilters:
					for _, filter := range vals {
						m.DeleteFilter(makeFilter(filter).ID)
					}
				}
				require.ElementsMatch(t, m.Environments(), cmd.expectedEnvs)
			}
		})
	}
}
