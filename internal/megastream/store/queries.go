package store

// The read surface. Every method here is a query one downstream concern needs, and each says
// which one, because a read API with no named consumer is a liability.
//
//   - Get        content for the byte-through surfaces: an object's verbatim JSON for stream
//                replay, polling and the eventual database writes.
//   - Members    the object set to serve for a credential, which is what a poll or a stream
//                replay enumerates.
//   - Serves     one object's visibility for a credential, for the per-item endpoints that
//                fetch a single flag or segment.
//   - ScopesFor  the fan-out for a content change: which scopes' downstream connections a
//                single put-object has to reach. It consults the reverse index and then the
//                unfiltered scopes, so the cost is one map lookup plus the scope count.
//   - Pending    the references still waiting on content, which the self-repair path reports
//                when they outlive their scope's cursor.
//   - State      the selector to present at the next handshake, and downstream as an Etag.
//                It is withheld while anything is pending, so the gate and the read cannot
//                come apart.
//   - Hydrated   whether a scope has had its first cursor, which the sweep and the
//                pending-ref grace period are both defined against.
//   - Scopes     the scopes the store has opened, for enumerating them when computing what a
//                post-reconnect sweep may evict.
//
// Ordering is deliberately unspecified. The one place that provably needs a stable order
// computes a hash over it and sorts for itself, so paying for a sort on every read here
// would buy a guarantee one caller needs and the rest do not. A caller that needs order
// sorts.
//
// These reads are per message rather than per delivery. A delta delivery is visible to a
// reader message by message, so a read landing inside one sees a partially applied
// membership. That is why State withholds its selector until the delivery's cursor: the
// selector is what pins a coherent state, and a caller that needs a coherent view reads it
// alongside the membership. Publishing whole deliveries atomically is a later increment.

// Get returns the record for an object, and reports whether the store holds it. An eviction
// removes the row, so a hit always carries content.
func (s *Store) Get(id ObjectID) (*Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.objects[id]
	return record, ok
}

// Members returns the objects a scope may see and the store has content for.
//
// Nothing is served before the scope's first cursor, which is its hydration-complete signal.
// That matters most for an unfiltered scope, which would otherwise serve the whole base store
// from its intent, before its own delivery had produced a single message.
//
// For an unfiltered scope the membership is everything the base store holds, computed on
// demand because such a scope carries no membership set. For a filtered scope it is its
// membership, minus anything with no content: a pending reference is membership without
// something to serve.
func (s *Store) Members(name string) []ObjectID {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sc := s.scopes[name]
	if sc == nil || !sc.hydrated {
		return nil
	}
	if sc.unfiltered {
		return s.presentLocked()
	}
	ids := make([]ObjectID, 0, len(sc.members))
	for id := range sc.members {
		if s.objects[id].Present() {
			ids = append(ids, id)
		}
	}
	return ids
}

// Serves reports whether a scope may see an object and the store has content to serve for
// it. This is the visibility contract per credential, in one place, and like Members it
// serves nothing before the scope's first cursor.
//
// The content check comes first and covers both kinds of scope. An unfiltered scope may see
// everything the base store holds, which is not the same as being able to serve something
// the base store does not hold -- so the membership test is the part that can be skipped for
// an unfiltered scope, and the content lookup is not.
func (s *Store) Serves(name string, id ObjectID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sc := s.scopes[name]
	if sc == nil || !sc.hydrated || !s.objects[id].Present() {
		return false
	}
	if sc.unfiltered {
		return true
	}
	_, member := sc.members[id]
	return member
}

// ScopesFor returns the scopes a content change to an object must reach: every scope
// referencing it, plus every unfiltered scope, which references everything without saying so.
func (s *Store) ScopesFor(id ObjectID) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.referencing[id])+1)
	for name := range s.referencing[id] {
		names = append(names, name)
	}
	for name, sc := range s.scopes {
		if sc.unfiltered {
			if _, already := s.referencing[id][name]; !already {
				names = append(names, name)
			}
		}
	}
	return names
}

// Pending returns the objects a scope references but the store holds no content for.
func (s *Store) Pending(name string) []ObjectID {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sc := s.scopes[name]
	if sc == nil {
		return nil
	}
	ids := make([]ObjectID, 0, len(sc.pending))
	for id := range sc.pending {
		ids = append(ids, id)
	}
	return ids
}

// State returns the selector a scope may present at the next handshake. The second result
// reports whether there is one.
//
// It is withheld in three cases. Before the scope's first cursor there is nothing recorded.
// After a refusal discarded it, there is nothing to present either -- and that is separate
// from the scope still serving its retained membership. And while any reference the scope
// holds is still waiting on content, the recorded selector is not safe to present: the state
// a relay presents is the only signal the server ever receives about what was applied, so
// offering one early would have the server skip exactly the messages this relay never got.
//
// Withholding is what makes this one operation rather than two. A separate gate and read
// would each take the lock, letting the applier advance between them, so a caller could
// check the gate against one selector and then present a later one the rule forbids.
func (s *Store) State(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sc := s.scopes[name]
	if sc == nil || !sc.hydrated || sc.state == "" || len(sc.pending) > 0 {
		return "", false
	}
	return sc.state, true
}

// Hydrated reports whether a scope has received its first cursor, which is the protocol's
// hydration-complete signal and the latch that makes the scope servable.
//
// This is deliberately separate from State. A scope can be hydrated with no selector to
// present -- a refusal discards the selector while the membership keeps being served -- and
// the post-reconnect sweep and the pending-ref grace period are both defined in terms of the
// cursor having arrived rather than in terms of having a selector.
func (s *Store) Hydrated(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sc := s.scopes[name]
	return sc != nil && sc.hydrated
}

// Scopes returns the names of every scope the store has opened.
func (s *Store) Scopes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.scopes))
	for name := range s.scopes {
		names = append(names, name)
	}
	return names
}

// presentLocked returns the ids the store holds content for. The caller holds the lock.
func (s *Store) presentLocked() []ObjectID {
	ids := make([]ObjectID, 0, len(s.objects))
	for id, record := range s.objects {
		if record.Present() {
			ids = append(ids, id)
		}
	}
	return ids
}
