package store

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
)

// A property-based check that the store's three indexes cannot drift apart, adapted from the
// harness the 2026-09-25 adversarial review used to find the rebuild-scoping bugs. Targeted
// tests pin the sequences we know about; this one covers the sequences we did not think of,
// which is where both of those bugs lived.
//
// The invariants are structural rather than behavioral, so they hold at every point in every
// legal message sequence:
//
//	I1  every member of a filtered scope has a reverse-index row naming that scope
//	I2  every reverse-index row names a scope that actually holds the object
//	I3  an unfiltered scope holds no membership and no pending
//	I4  a scope's pending set is exactly its members the store has no content for
//	I5  every pending entry names an identity an object message could address, since the
//	    reverse index is what a put walks to clear it
//	I6  a delivery declared unfiltered builds no rebuild buffer, since no refs may arrive
//	I7  a declaration is staged only for a scope that has hydrated, since one that has not
//	    serves nothing and takes its declaration at once
//	I8  State presents only the scope's latest accepted cursor, and only while nothing is
//	    pending or evicted and the membership has not changed since that cursor (checked
//	    against a shadow record in the test loop, outside the lock)
//	I9  a scope's evicted set names only objects the store does not hold, and its created
//	    set only objects it does hold; only an unfiltered scope has either set
//	I10 the unfiltered index names exactly the scopes served as unfiltered
//	I11 only a filtered scope with a selector retracts or adds, an added object is a live
//	    member, a retracted object is not, and no object is both added and retracted
func checkInvariants(t *testing.T, s *Store, step int, seed uint64) {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()

	fail := func(format string, args ...any) {
		t.Fatalf("seed %d, step %d: %s", seed, step, fmt.Sprintf(format, args...))
	}

	for name, sc := range s.scopes {
		// I6
		if sc.declaredUnfiltered && sc.rebuilding != nil {
			fail("I6: scope %q is declared unfiltered but holds a rebuild buffer", name)
		}
		// I9
		for id := range sc.created {
			if !sc.unfiltered {
				fail("I9: filtered scope %q has created %s", name, id)
			}
			if !s.objects[id].Present() {
				fail("I9: scope %q has created %s, which the store does not hold", name, id)
			}
		}
		// I10
		if indexed := s.unfiltered[name] == sc; indexed != sc.unfiltered {
			fail("I10: scope %q unfiltered=%v but indexed=%v", name, sc.unfiltered, indexed)
		}
		for id := range sc.evicted {
			if !sc.unfiltered {
				fail("I9: filtered scope %q has evicted %s", name, id)
			}
			if s.objects[id].Present() {
				fail("I9: scope %q has evicted %s, which the store holds", name, id)
			}
		}
		// I11
		if len(sc.retracted) > 0 && (sc.unfiltered || sc.state == "") {
			fail("I11: scope %q retracts but is unfiltered or has no state", name)
		}
		if len(sc.added) > 0 && (sc.unfiltered || sc.state == "") {
			fail("I11: scope %q adds but is unfiltered or has no state", name)
		}
		for id := range sc.added {
			if _, member := sc.members[id]; !member {
				fail("I11: scope %q added %s, which it does not hold", name, id)
			}
			if _, retracted := sc.retracted[id]; retracted {
				fail("I11: scope %q both added and retracted %s", name, id)
			}
		}
		for id := range sc.retracted {
			if _, member := sc.members[id]; member {
				fail("I11: scope %q retracted %s, which it holds", name, id)
			}
		}
		// I7
		if !sc.hydrated && sc.declaredUnfiltered != sc.unfiltered {
			fail("I7: unhydrated scope %q has a staged declaration", name)
		}
		if sc.unfiltered {
			// I3
			if len(sc.members) > 0 {
				fail("I3: unfiltered scope %q holds membership %v", name, sc.members)
			}
			if len(sc.pending) > 0 {
				fail("I3: unfiltered scope %q holds pending %v", name, sc.pending)
			}
			continue
		}
		for id := range sc.members {
			// I1
			if _, ok := s.referencing[id][name]; !ok {
				fail("I1: scope %q holds %s with no reverse-index row", name, id)
			}
			// I4, first half
			_, pending := sc.pending[id]
			if present := s.objects[id].Present(); present == pending {
				fail("I4: scope %q holds %s present=%v pending=%v", name, id, present, pending)
			}
		}
		for id := range sc.pending {
			// I4, second half
			if _, member := sc.members[id]; !member {
				fail("I4: scope %q has %s pending without holding it", name, id)
			}
			// I5
			if id.Kind == "" || id.Key == "" {
				fail("I5: scope %q waits on %q, which no object message can address", name, id)
			}
		}
	}

	// I10, second half
	for name, sc := range s.unfiltered {
		if s.scopes[name] != sc {
			fail("I10: the unfiltered index names %q, which is not that scope", name)
		}
	}

	// I2
	for id, scopes := range s.referencing {
		if len(scopes) == 0 {
			fail("I2: %s has an empty reverse-index row", id)
		}
		for name := range scopes {
			sc := s.scopes[name]
			if sc == nil {
				fail("I2: %s names removed scope %q", id, name)
			}
			if _, member := sc.members[id]; !member {
				fail("I2: %s names scope %q, which does not hold it", id, name)
			}
		}
	}
}

// checkState verifies I8 through the public read, against the latest cursor the store
// accepted for each scope.
func checkState(t *testing.T, s *Store, latest map[string]string, step int, seed uint64) {
	t.Helper()
	for _, name := range s.Scopes() {
		state, ok := s.State(name)
		if !ok {
			continue
		}
		if pending := s.Pending(name); len(pending) > 0 {
			t.Fatalf("seed %d, step %d: I8: %q presents %q with pending %v",
				seed, step, name, state, pending)
		}
		s.mu.RLock()
		evicted := len(s.scopes[name].evicted)
		changed := s.scopes[name].changedSinceCursor()
		s.mu.RUnlock()
		if evicted > 0 {
			t.Fatalf("seed %d, step %d: I8: %q presents %q with %d evicted objects",
				seed, step, name, state, evicted)
		}
		if changed {
			t.Fatalf("seed %d, step %d: I8: %q presents %q with membership changed since it",
				seed, step, name, state)
		}
		if state != latest[name] {
			t.Fatalf("seed %d, step %d: I8: %q presents %q, but its latest cursor is %q",
				seed, step, name, state, latest[name])
		}
	}
}

// randomMessage produces one message for the given scope and object pools.
//
// Some are contract violations on purpose -- a ref for a scope declared unfiltered, an
// identity with an empty field. A rejected message is a legitimate outcome; what the
// invariants check is that the indexes are left consistent either way.
func randomMessage(rng *rand.Rand, scopes, keys []string) any {
	scope := scopes[rng.IntN(len(scopes))]
	key := keys[rng.IntN(len(keys))]
	if rng.IntN(15) == 0 {
		// An identity no object message could ever address. Admitting one used to strand a
		// pending entry that nothing could clear, which blocked the scope's selector for
		// the rest of the session.
		key = ""
	}
	kind := KindFlag
	switch rng.IntN(8) {
	case 0:
		kind = KindSegment
	case 1:
		// Kinds are an open set, so an unrecognized one must travel like a known one.
		kind = "ai-config"
	}

	switch rng.IntN(10) {
	case 0, 1:
		code := wire.IntentTransferFull
		switch rng.IntN(3) {
		case 1:
			code = wire.IntentTransferChanges
		case 2:
			code = wire.IntentNone
		}
		return intent(scope, code, rng.IntN(4) == 0)
	case 2, 3, 4:
		return ref(scope, kind, key)
	case 5:
		return refDelete(scope, kind, key)
	case 6:
		return wire.RefBatch{
			T: wire.TypeRefBatch, Scope: scope,
			Refs:    []wire.ObjectRef{{Kind: kind, Key: key}},
			Deletes: []wire.ObjectRef{{Kind: kind, Key: keys[rng.IntN(len(keys))]}},
		}
	case 7, 8:
		if rng.IntN(20) == 0 {
			// A content-less put: required by the schema, and accepting one used to clear
			// pending while holding nothing, leaving a member neither servable nor pending.
			return wire.PutObject{
				T: wire.TypePutObject, Base: base, Kind: kind, Key: key,
				Version: wire.PayloadVersion(rng.IntN(50)),
			}
		}
		return put(kind, key, rng.IntN(50), `{"key":"`+key+`","version":1}`)
	default:
		return del(kind, key, rng.IntN(50))
	}
}

func TestIndexInvariantsHoldAcrossRandomMessageSequences(t *testing.T) {
	scopes := []string{"sdk-env", "sdk-viewA", "sdk-viewB", "mob-viewA"}
	keys := []string{"f1", "f2", "f3", "f4", "s1", "s2"}

	for seed := uint64(1); seed <= 200; seed++ {
		s := New(discardLogger())
		rng := rand.New(rand.NewPCG(seed, seed*7919))
		latest := map[string]string{}

		for step := range 300 {
			switch {
			case rng.IntN(60) == 0:
				name := scopes[rng.IntN(len(scopes))]
				s.RemoveScope(name)
				delete(latest, name)
			case rng.IntN(90) == 0:
				s.DiscardState(scopes[rng.IntN(len(scopes))])
			default:
				msg := randomMessage(rng, scopes, keys)
				// A cursor needs a scope that exists, so drive it explicitly rather than
				// leaving it to chance.
				if rng.IntN(6) == 0 {
					msg = cursor(scopes[rng.IntN(len(scopes))], fmt.Sprintf("sel-%d", step), step)
				}
				// Errors are expected, since the generator produces contract violations on
				// purpose. What matters is that a rejected message leaves the indexes
				// consistent, which the check below verifies either way.
				if s.Apply(msg) == nil {
					if c, ok := msg.(wire.PayloadTransferred); ok && s.Hydrated(c.Scope) {
						latest[c.Scope] = c.State
					}
				}
			}
			checkInvariants(t, s, step, seed)
			checkState(t, s, latest, step, seed)
		}

		// Reads must not panic or disagree with the indexes at the end of a sequence.
		for _, name := range s.Scopes() {
			for _, id := range s.Members(name) {
				require.True(t, s.Serves(name, id),
					"seed %d: %q serves %s per Members but not per Serves", seed, name, id)
			}
		}
	}
}

// A record is published once and never edited, so a reader may hold one across any number of
// parses while the applier replaces and evicts the object underneath it. This is the property
// that lets the lazy parse take no lock beyond its own.
func TestLazyParseIsSafeWhileTheApplierReplacesRecords(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		flagPut("f1", 1),
		cursor("sdk-env", "sel-1", 1),
	)

	id := ObjectID{KindFlag, "f1"}
	const updates = 300
	writing := make(chan struct{})
	go func() {
		defer close(writing)
		for i := range updates {
			assert.NoError(t, s.Apply(flagPut("f1", i)))
			if i%10 == 0 {
				assert.NoError(t, s.Apply(del(KindFlag, "f1", i)))
			}
		}
	}()

	readers := 8
	done := make(chan struct{}, readers)
	for range readers {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case <-writing:
					return
				default:
				}
				// Hold one record across repeated parses: the memoized result must stay
				// consistent even as the applier publishes replacements.
				record, ok := s.Get(id)
				if !ok {
					continue
				}
				first, err := record.Item()
				for range 8 {
					again, againErr := record.Item()
					assert.Equal(t, first, again)
					assert.Equal(t, err, againErr)
				}
				_ = record.Bytes()
				_ = record.Version()
			}
		}()
	}

	<-writing
	for range readers {
		<-done
	}
}
