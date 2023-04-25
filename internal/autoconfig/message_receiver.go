package autoconfig

import "github.com/launchdarkly/go-sdk-common/v3/ldlog"

// MessageReceiver is responsible for transforming a potentially unreliable stream of SSE message from LaunchDarkly
// into a reliable sequence of commands for other components. It serves to isolate the state needed for those operations
// into a single place, so that other parts of the code can simply process commands in the order they arrive.
//
// As a motivating example, the following messages are received in order:
//
//	Message 1: {delete XYZ, version 2}
//	Message 2: {upsert XYZ, version 1}
//
// This component must discard Message 2, since its version number is <= than a previous message. The final result
// is that XYZ should not exist in the system.
type MessageReceiver[T Item] struct {
	seen    map[string]*versioned[T]
	loggers ldlog.Loggers
}

// An Item is anything that can report its own ID and a human-readable description of itself.
// Its allows the MessageReceiver to maintain a list of "seen" items.
type Item interface {
	// Describe is the human-readable identifier for the item, used in log messages.
	Describe() string
}

// An Action represents actions generated in response to invoking MessageReceiver's methods, which a caller should
// handle.
// Returned Actions will obey the following order constraints:
//  1. An item will never be Updated or Deleted without first being Inserted.
//  2. An item may be Updated 0 or more times.
//  3. An item will be Deleted exactly once.
//
// At any point, a Noop may be emitted, in which case the caller may take no action.
type Action string

const (
	ActionInsert = Action("insert")
	ActionUpdate = Action("updated")
	ActionDelete = Action("delete")
	ActionNoop   = Action("noop")
)

type versioned[T Item] struct {
	// The item itself.
	item T
	// Version assigned to this item.
	version int
	// Whether the item is "soft deleted" (aka tombstone technique).
	// Necessary to filter out stale insert/update requests that may arrive after a deletion.
	entombed bool
}

func newVersioned[T Item](item T, version int) *versioned[T] {
	return &versioned[T]{item: item, version: version, entombed: false}
}

func newTombstone[T Item](version int) *versioned[T] {
	return &versioned[T]{version: version, entombed: true}
}

// Converts an item into a tombstone, with the given version.
func (v *versioned[T]) entomb(version int) {
	v.entombed = true
	v.version = version
}

// Updates an item, returning true if the item was previously a tombstone.
func (v *versioned[T]) update(item T, version int) bool {
	v.item = item
	v.version = version
	resurrected := v.entombed
	v.entombed = false
	return resurrected
}

func NewMessageReceiver[T Item](loggers ldlog.Loggers) *MessageReceiver[T] {
	return &MessageReceiver[T]{
		seen:    make(map[string]*versioned[T]),
		loggers: loggers,
	}
}

// Upsert receives an item and version, conditionally forwarding it to the underlying ItemReceiver.
func (v *MessageReceiver[T]) Upsert(id string, item T, version int) Action {
	current, seen := v.seen[id]

	// Never-before-seen items should be inserted.
	if !seen {
		v.seen[id] = newVersioned(item, version)
		v.loggers.Infof(logMsgAddEnv, item.Describe())
		return ActionInsert
	}

	// Out-of-order messages have no effect, but could indicate something odd.
	if version <= current.version {
		v.loggers.Debugf(logMsgUpdateBadVersion, item.Describe())
		return ActionNoop
	}

	// If the item was previously a tombstone, then this is really an insert - rather than an update - since we've
	// had an intervening delete.
	if resurrected := current.update(item, version); resurrected {
		v.loggers.Infof(logMsgAddEnv, item.Describe())
		return ActionInsert
	} else {
		v.loggers.Infof(logMsgUpdateEnv, item.Describe())
		return ActionUpdate
	}
}

// Delete receives an item ID and version, conditionally forwarding the request to the underlying ItemReceiver.
func (v *MessageReceiver[T]) Delete(id string, version int) Action {
	current, seen := v.seen[id]

	// Never-before-seen items generate a tombstone.
	// For example, receiving {delete #123, v2} followed by {upsert #123, v1} should result in no
	// insertion, because the upsert came later. We can't prove that without storing a tombstone.
	if !seen {
		v.seen[id] = newTombstone[T](version)
		return ActionNoop
	}

	// Out-of-order messages have no effect, but could indicate something odd.
	if version <= current.version {
		// Not using current.item.Describe() because if this was constructed using newTombstone,
		// then item will be zero-valued.
		v.loggers.Debugf(logMsgDeleteBadVersion, id)
		return ActionNoop
	}

	// Since version > current.version, accept the message.
	// If the item isn't a tombstone, entomb it and forward the deletion to the sink; otherwise
	// increase the existing tombstone's version number.

	if !current.entombed {
		v.loggers.Infof(logMsgDeleteEnv, current.item.Describe())
		current.entomb(version)
		return ActionDelete
	}

	current.version = version
	return ActionNoop
}

// Forget causes MessageReceiver to behave as if the ID was never seen before. It may invoke the ItemReceiver's
// delete command if the item exists.
func (v *MessageReceiver[T]) Forget(id string) Action {
	action := ActionNoop
	if current, seen := v.seen[id]; seen {
		if !current.entombed {
			v.loggers.Infof(logMsgDeleteEnv, current.item.Describe())
			action = ActionDelete
		}
		delete(v.seen, id)
	}
	return action
}

// Purge calls Forget on all ids for which the predicate returns true. Returns the IDs of all items
// that should be deleted.
func (v *MessageReceiver[T]) Purge(predicate func(id string) bool) []string {
	var deleted []string
	for seen := range v.seen {
		if predicate(seen) && v.Forget(seen) == ActionDelete {
			deleted = append(deleted, seen)
		}
	}
	return deleted
}

// Retain keeps all ids for which the predicate returns true, and calls Forget
// on the rest. Returns the IDs of all items that should be deleted.
func (v *MessageReceiver[T]) Retain(predicate func(id string) bool) []string {
	return v.Purge(func(id string) bool {
		return !predicate(id)
	})
}
