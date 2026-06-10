package autoconfig

import (
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
)

type testItem string

func (t testItem) Describe() string {
	return string(t)
}

func TestMessageReceiver_CommandSequences(t *testing.T) {
	type action string
	const (
		delete = action("delete")
		upsert = action("upsert")
		forget = action("forget")
	)
	type cmd struct {
		cmd      action
		version  int
		expected Action
	}
	type scenario struct {
		name string
		cmds []cmd
	}

	scenarios := []scenario{
		{
			name: "deleting nonexistent item is a noop",
			cmds: []cmd{
				{
					cmd:      delete,
					version:  0,
					expected: ActionNoop,
				},
			},
		},
		{
			name: "forgetting nonexistent item is a noop",
			cmds: []cmd{
				{
					cmd:      forget,
					expected: ActionNoop,
				},
			},
		},
		{
			name: "deleting item twice is a noop",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  0,
					expected: ActionInsert,
				},
				{
					cmd:      delete,
					version:  1,
					expected: ActionDelete,
				},
				{
					cmd:      delete,
					version:  2,
					expected: ActionNoop,
				},
			},
		},
		{
			name: "upsert after upsert is an update",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  0,
					expected: ActionInsert,
				},
				{
					cmd:      upsert,
					version:  1,
					expected: ActionUpdate,
				},
				{
					cmd:      upsert,
					version:  2,
					expected: ActionUpdate,
				},
			},
		},
		{
			name: "upsert after delete is an insert",
			cmds: []cmd{
				{
					cmd:      delete,
					version:  0,
					expected: ActionNoop,
				},
				{
					cmd:      upsert,
					version:  1,
					expected: ActionInsert,
				},
			},
		},
		{
			name: "upsert after deletion is an insert",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  0,
					expected: ActionInsert,
				},
				{
					cmd:      delete,
					version:  1,
					expected: ActionDelete,
				},
				{
					cmd:      upsert,
					version:  2,
					expected: ActionInsert,
				},
			},
		},
		{
			name: "out-of-order upsert after delete is noop",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  0,
					expected: ActionInsert,
				},
				{
					cmd:      delete,
					version:  2,
					expected: ActionDelete,
				},
				{
					cmd:      upsert,
					version:  1,
					expected: ActionNoop,
				},
			},
		},
		{
			name: "upsert after forgetting is an insert",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  0,
					expected: ActionInsert,
				},
				{
					cmd:      forget,
					expected: ActionDelete,
				},
				{
					cmd:      upsert,
					version:  0, // version can be 0 (rather than 1) because of the forget command.
					expected: ActionInsert,
				},
			},
		},
		{
			name: "out-of-order upsert is a noop",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  1,
					expected: ActionInsert,
				},
				{
					cmd:      upsert,
					version:  0,
					expected: ActionNoop,
				},
			},
		},
		{
			name: "out-of-order upsert is ignored following delete",
			cmds: []cmd{
				{
					cmd:      delete,
					version:  1,
					expected: ActionNoop,
				},
				{
					cmd:      upsert,
					version:  0,
					expected: ActionNoop,
				},
			},
		},
		{
			name: "out-of-order delete is a noop",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  1,
					expected: ActionInsert,
				},
				{
					cmd:      delete,
					version:  0,
					expected: ActionNoop,
				},
			},
		},

		{
			name: "forget only deletes once",
			cmds: []cmd{
				{
					cmd:      upsert,
					version:  0,
					expected: ActionInsert,
				},
				{
					cmd:      forget,
					expected: ActionDelete,
				},
				{
					cmd:      forget,
					expected: ActionNoop,
				},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			mockLog := ldlogtest.NewMockLog()
			defer mockLog.DumpIfTestFailed(t)
			mockLog.Loggers.SetMinLevel(ldlog.Debug)

			rec := NewMessageReceiver[testItem](mockLog.Loggers)

			const id = "id"
			const value = testItem("value")

			for _, cmd := range scenario.cmds {
				var action Action
				switch cmd.cmd {
				case delete:
					action = rec.Delete(id, cmd.version)
				case upsert:
					action = rec.Upsert(id, value, cmd.version)
				case forget:
					action = rec.Forget(id)
				default:
					t.Fatalf("unrecognized test cmd: %s", cmd.cmd)
				}
				require.Equal(t, cmd.expected, action,
					"given command (%s), expected to receive action (%s) but got (%s)", cmd.cmd, cmd.expected, action)
			}
		})
	}
}

func TestMessageReceiver_Purge(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	type scenario struct {
		name     string
		upsert   []string
		delete   []string
		expected []string
	}

	scenarios := []scenario{
		{
			name:     "delete nonmember element",
			upsert:   []string{"a", "b", "c"},
			delete:   []string{"d"},
			expected: []string{},
		},
		{
			name:     "delete empty set",
			upsert:   []string{"a", "b", "c"},
			delete:   []string{},
			expected: []string{},
		},
		{
			name:     "delete equal set",
			upsert:   []string{"a", "b", "c"},
			delete:   []string{"b", "a", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "delete subset",
			upsert:   []string{"a", "b", "c"},
			delete:   []string{"a"},
			expected: []string{"a"},
		},
		{
			name:     "delete superset",
			upsert:   []string{"a", "b", "c"},
			delete:   []string{"a", "b", "c", "d"},
			expected: []string{"a", "b", "c"},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			rec := NewMessageReceiver[testItem](mockLog.Loggers)

			for _, id := range scenario.upsert {
				rec.Upsert(id, "arbitrary", 0)
			}

			shouldDelete := func(id string) bool {
				return slices.Contains(scenario.delete, id)
			}

			require.ElementsMatch(t, scenario.expected, rec.Purge(shouldDelete))
		})
	}

}

// The Purge and Retain functions should be inverses of each-other.
// If you have elements [a, b, c], then purging 'a' should result in the same elements deleted as retaining all but 'a'.
// To attempt to disprove this property, the quick.Check function is used to upsert arbitrary elements
// into two MessageReceivers, and then Purge and Retain (w/ an inverted predicate) are called.
// The list of deleted elements reported by the functions should match.
func TestNewMessageReceiver_RetainAndPurgeAreInverse(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	subset := func(items []string, probability float64) []string {
		var selected []string
		for _, item := range items {
			if rand.Float64() > probability {
				selected = append(selected, item)
			}
		}
		return selected
	}

	inverseProperty := func(ids []string) bool {

		purge := NewMessageReceiver[testItem](mockLog.Loggers)
		retain := NewMessageReceiver[testItem](mockLog.Loggers)

		for _, id := range ids {
			purge.Upsert(id, "arbitrary", 0)
			retain.Upsert(id, "arbitrary", 0)
		}

		// Delete ~half of the items.
		shouldDelete := subset(ids, 0.5)

		deletePred := func(id string) bool {
			return slices.Contains(shouldDelete, id)
		}

		retainPred := func(id string) bool { return !deletePred(id) }

		purgeDeleted := purge.Purge(deletePred)
		retainDeleted := retain.Retain(retainPred)

		slices.Sort(purgeDeleted)
		slices.Sort(retainDeleted)

		return slices.Equal(purgeDeleted, retainDeleted)
	}

	if err := quick.Check(inverseProperty, nil); err != nil {
		t.Fatal(err)
	}
}
