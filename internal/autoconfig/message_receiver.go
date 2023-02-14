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
	sink    ItemReceiver[T]
	loggers ldlog.Loggers
}

// An Item is anything that can report its own ID and a human-readable description of itself.
// Its allows the MessageReceiver to maintain a list of "seen" items.
type Item interface {
	// Describe is the human-readable identifier for the item, used in log messages.
	Describe() string
}

// ItemReceiver is any component capable of receiving new items, changes to existing items,
// or requests to delete an item.
//
// Calls to the methods will follow these constraints:
//  1. An item will never be Updated or Deleted without first being Inserted.
//  2. An item may be Updated 0 or more times.
//  3. An item will be Deleted exactly once.
type ItemReceiver[T any] interface {
	// Insert inserts a new item.
	Insert(item T)
	// Update changes an existing item.
	Update(item T)
	// Delete removes an existing item.
	Delete(id string)
}

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

func NewMessageReceiver[T Item](sink ItemReceiver[T], loggers ldlog.Loggers) *MessageReceiver[T] {
	return &MessageReceiver[T]{
		seen:    make(map[string]*versioned[T]),
		sink:    sink,
		loggers: loggers,
	}
}

// Upsert receives an item and version, conditionally forwarding it to the underlying ItemReceiver.
func (v *MessageReceiver[T]) Upsert(id string, item T, version int) {
	current, seen := v.seen[id]

	// Never-before-seen items should be inserted.
	if !seen {
		v.seen[id] = newVersioned(item, version)
		v.loggers.Infof(logMsgAddEnv, item.Describe())
		v.sink.Insert(item)
		return
	}

	// Out-of-order messages have no effect, but could indicate something odd.
	if version <= current.version {
		v.loggers.Infof(logMsgUpdateBadVersion, item.Describe())
		return
	}

	// If the item was previously a tombstone, then this is really an insert - rather than an update - since we've
	// had an intervening delete.
	if resurrected := current.update(item, version); resurrected {
		v.loggers.Infof(logMsgAddEnv, item.Describe())
		v.sink.Insert(item)
	} else {
		v.loggers.Infof(logMsgUpdateEnv, item.Describe())
		v.sink.Update(item)
	}
}

// Delete receives an item ID and version, conditionally forwarding the request to the underlying ItemReceiver.
func (v *MessageReceiver[T]) Delete(id string, version int) {
	current, seen := v.seen[id]

	// Never-before-seen items generate a tombstone.
	// For example, receiving {delete #123, v2} followed by {upsert #123, v1} should result in no
	// insertion, because the upsert came later. We can't prove that without storing a tombstone.
	if !seen {
		v.seen[id] = newTombstone[T](version)
		return
	}

	// Out-of-order messages have no effect, but could indicate something odd.
	if version <= current.version {
		// Not using current.item.Describe() because if this was constructed using newTombstone,
		// then item will be zero-valued.
		v.loggers.Infof(logMsgDeleteBadVersion, id)
		return
	}

	// Since version > current.version, accept the message.
	// If the item isn't a tombstone, entomb it and forward the deletion to the sink; otherwise
	// increase the existing tombstone's version number.

	if !current.entombed {
		v.loggers.Infof(logMsgDeleteEnv, current.item.Describe())
		current.entomb(version)
		v.sink.Delete(id)
		return
	}

	current.version = version
}

// Forget causes MessageReceiver to behave as if the ID was never seen before. It may invoke the ItemReceiver's
// delete command if the item exists.
func (v *MessageReceiver[T]) Forget(id string) {
	if current, seen := v.seen[id]; seen {
		if !current.entombed {
			v.loggers.Infof(logMsgDeleteEnv, current.item.Describe())
			v.sink.Delete(id)
		}
		delete(v.seen, id)
	}
}

// Purge calls Forget on all ids for which the predicate returns true.
func (v *MessageReceiver[T]) Purge(purge func(id string) bool) {
	for seen := range v.seen {
		if purge(seen) {
			v.Forget(seen)
		}
	}
}

// Retain keeps all ids for which the predicate returns true, and calls Forget
// on the rest.
func (v *MessageReceiver[T]) Retain(retain func(id string) bool) {
	v.Purge(func(id string) bool {
		return !retain(id)
	})
}
