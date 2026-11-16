package store

import (
	"log/slog"
	"testing"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
)

const base = "payload-1"

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(discardLogger())
}

// apply drives a message sequence, failing on the first message the store refuses.
func apply(t *testing.T, s *Store, msgs ...any) {
	t.Helper()
	for i, msg := range msgs {
		require.NoError(t, s.Apply(msg), "message %d (%T)", i, msg)
	}
}

func intent(scope, code string, unfiltered bool) wire.ServerIntent {
	return wire.ServerIntent{
		T: wire.TypeServerIntent, Scope: scope, IntentCode: code, Unfiltered: &unfiltered,
	}
}

func put(kind, key string, version int, body string) wire.PutObject {
	return wire.PutObject{
		T: wire.TypePutObject, Base: base, Kind: kind, Key: key,
		Version: wire.PayloadVersion(version), Object: []byte(body),
	}
}

func flagPut(key string, version int) wire.PutObject {
	return put(KindFlag, key, version, `{"key":"`+key+`","version":1,"on":true}`)
}

func del(kind, key string, version int) wire.DeleteObject {
	return wire.DeleteObject{
		T: wire.TypeDeleteObject, Base: base, Kind: kind, Key: key,
		Version: wire.PayloadVersion(version),
	}
}

func ref(scope, kind, key string) wire.Ref {
	return wire.Ref{T: wire.TypeRef, Scope: scope, Kind: kind, Key: key}
}

func refDelete(scope, kind, key string) wire.RefDelete {
	return wire.RefDelete{T: wire.TypeRefDelete, Scope: scope, Kind: kind, Key: key}
}

func cursor(scope, state string, version int) wire.PayloadTransferred {
	return wire.PayloadTransferred{
		T: wire.TypePayloadTransferred, Scope: scope,
		State: state, Version: wire.PayloadVersion(version),
	}
}

func ids(values ...string) []ObjectID {
	out := make([]ObjectID, 0, len(values))
	for _, v := range values {
		for i := range len(v) {
			if v[i] == '/' {
				out = append(out, ObjectID{Kind: v[:i], Key: v[i+1:]})
				break
			}
		}
	}
	return out
}

// An unfiltered scope declares implicit full membership and receives no refs, so its
// membership is whatever the base store holds.
func TestUnfilteredScopeSeesTheWholeBase(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		flagPut("f1", 19), flagPut("f2", 19), put(KindSegment, "s1", 19, `{"key":"s1","version":1}`),
		cursor("sdk-env", "sel-1", 19),
	)

	assert.ElementsMatch(t, ids("flag/f1", "flag/f2", "segment/s1"), s.Members("sdk-env"))

	state, hydrated := s.State("sdk-env")
	assert.True(t, hydrated)
	assert.Equal(t, "sel-1", state)
}

// A filtered scope sees only what its refs name, even though the base store holds more. This
// is the visibility contract per credential.
func TestFilteredScopeSeesOnlyItsMembers(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		ref("sdk-viewA", KindSegment, "s1"),
		flagPut("f1", 19), flagPut("f2", 19), flagPut("f9", 19),
		put(KindSegment, "s1", 19, `{"key":"s1","version":1}`),
		cursor("sdk-env", "sel-env", 19),
		cursor("sdk-viewA", "sel-a", 19),
	)

	assert.ElementsMatch(t, ids("flag/f1", "segment/s1"), s.Members("sdk-viewA"))
	assert.Len(t, s.Members("sdk-env"), 4)

	assert.True(t, s.Serves("sdk-viewA", ObjectID{KindFlag, "f1"}))
	assert.False(t, s.Serves("sdk-viewA", ObjectID{KindFlag, "f9"}),
		"f9 is in the base store but not in this scope's membership")
	assert.True(t, s.Serves("sdk-env", ObjectID{KindFlag, "f9"}))
}

// An empty view also has zero refs, so inferring full membership from their absence would
// turn an empty scope into full data exposure.
func TestMembershipIsNeverInferredFromAbsentRefs(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("sdk-empty", wire.IntentTransferFull, false),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-env", "sel-env", 19),
		cursor("sdk-empty", "sel-empty", 19),
	)

	assert.Empty(t, s.Members("sdk-empty"))
	assert.False(t, s.Serves("sdk-empty", ObjectID{KindFlag, "f1"}))
	assert.Len(t, s.Members("sdk-env"), 2, "the unfiltered scope is unaffected")
}

// Refs assert membership, not existence. One naming an object the store does not hold yet is
// pending membership rather than an error, and the content resolves it.
func TestARefBeforeItsContentIsPending(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		cursor("sdk-viewA", "sel-a", 19),
	)

	assert.ElementsMatch(t, ids("flag/f1"), s.Pending("sdk-viewA"))
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable,
		"a selector is withheld while a reference is outstanding")
	assert.Empty(t, s.Members("sdk-viewA"), "membership with nothing to serve yet")

	// The content lands in a later delivery and promotes the reference.
	apply(t, s, flagPut("f1", 20))

	assert.Empty(t, s.Pending("sdk-viewA"))
	_, presentable = s.State("sdk-viewA")
	assert.True(t, presentable, "the selector is presentable once nothing is pending")
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// Content and membership are separate planes. Evicting an object leaves every scope's
// membership alone; the reference simply becomes pending again.
func TestDeleteObjectLeavesMembershipUntouched(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19),
		cursor("sdk-env", "sel-env", 19),
		cursor("sdk-viewA", "sel-a", 19),
	)
	require.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))

	apply(t, s, del(KindFlag, "f1", 20))

	assert.ElementsMatch(t, ids("flag/f1"), s.Pending("sdk-viewA"),
		"the membership entry survives the eviction and is pending again")
	assert.Empty(t, s.Members("sdk-viewA"), "there is nothing to serve for it")
	assert.Empty(t, s.Members("sdk-env"))

	// Re-entry always carries fresh content, because no updates flow to a non-member while
	// an object is outside the union.
	apply(t, s, flagPut("f1", 21))
	assert.Empty(t, s.Pending("sdk-viewA"))
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// A pending reference outliving its content is the same normal transient. Only a ref-delete,
// a scope removal, or a resync that does not re-assert it takes it away.
func TestDeleteObjectDoesNotDiscardPendingMembership(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19),
		cursor("sdk-viewA", "sel-a", 19),
		del(KindFlag, "f1", 20),
	)
	require.ElementsMatch(t, ids("flag/f1"), s.Pending("sdk-viewA"))

	// A second eviction changes nothing, and neither does one for an object never held.
	apply(t, s, del(KindFlag, "f1", 21), del(KindFlag, "never-held", 21))
	assert.ElementsMatch(t, ids("flag/f1"), s.Pending("sdk-viewA"))

	apply(t, s, refDelete("sdk-viewA", KindFlag, "f1"))
	assert.Empty(t, s.Pending("sdk-viewA"))

	// The selector still claims f1, which the store no longer holds, so it waits for the
	// cursor that covers the ref-delete.
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable)
	apply(t, s, cursor("sdk-viewA", "sel-b", 21))
	state, presentable := s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-b", state)
}

// A filtered scope that drops an object and then sees it evicted, both before its cursor,
// has a selector that still claims the object. Presenting that selector would let the server
// count the object as held, and send another scope a reference to it with no content.
func TestAnEvictionAfterARefDeleteWithholdsTheSelector(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "k7"),
		ref("sdk-viewA", KindFlag, "k8"),
		flagPut("k7", 7),
		flagPut("k8", 7),
		cursor("sdk-viewA", "sel-1", 7),
		refDelete("sdk-viewA", KindFlag, "k7"),
	)
	state, presentable := s.State("sdk-viewA")
	require.True(t, presentable, "a ref-delete alone loses nothing the store holds")
	require.Equal(t, "sel-1", state)

	apply(t, s, del(KindFlag, "k7", 8))
	_, presentable = s.State("sdk-viewA")
	assert.False(t, presentable)

	// Fresh content satisfies the claim again.
	apply(t, s, flagPut("k7", 9))
	state, presentable = s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-1", state)

	// So does the cursor that covers the change.
	apply(t, s, del(KindFlag, "k7", 10))
	_, presentable = s.State("sdk-viewA")
	assert.False(t, presentable)
	apply(t, s, cursor("sdk-viewA", "sel-2", 10))
	state, presentable = s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-2", state)

	// The cursor ends the watch, so a later eviction of k7 does not concern the scope.
	apply(t, s, flagPut("k7", 11), del(KindFlag, "k7", 12))
	_, presentable = s.State("sdk-viewA")
	assert.True(t, presentable)
}

// The eviction of an object another scope dropped does not concern this scope.
func TestAnEvictionConcernsOnlyTheScopeThatDroppedTheObject(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		intent("sdk-viewB", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "k7"),
		ref("sdk-viewB", KindFlag, "k7"),
		flagPut("k7", 7),
		cursor("sdk-viewA", "sel-a", 7),
		cursor("sdk-viewB", "sel-b", 7),
		refDelete("sdk-viewA", KindFlag, "k7"),
		refDelete("sdk-viewB", KindFlag, "k7"),
		cursor("sdk-viewB", "sel-b2", 8),
		del(KindFlag, "k7", 8),
	)
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable)
	state, presentable := s.State("sdk-viewB")
	assert.True(t, presentable)
	assert.Equal(t, "sel-b2", state)
}

// An object referenced after the cursor is not one the selector claims, so dropping it and
// then evicting it does not withhold the selector.
func TestAnEvictionOfAnObjectAddedAfterTheCursorKeepsTheSelector(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "k8"),
		flagPut("k8", 7),
		cursor("sdk-viewA", "sel-1", 7),
		ref("sdk-viewA", KindFlag, "k7"),
		flagPut("k7", 8),
		refDelete("sdk-viewA", KindFlag, "k7"),
		del(KindFlag, "k7", 9),
	)
	state, presentable := s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-1", state)

	// An object the selector claims stays watched after it is dropped, referenced again and
	// dropped again.
	apply(t, s,
		refDelete("sdk-viewA", KindFlag, "k8"),
		ref("sdk-viewA", KindFlag, "k8"),
		refDelete("sdk-viewA", KindFlag, "k8"),
		del(KindFlag, "k8", 10),
	)
	_, presentable = s.State("sdk-viewA")
	assert.False(t, presentable)
}

// Removing a scope or discarding its state ends the watch with the selector.
func TestRemovingOrDiscardingAScopeEndsTheWatch(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		intent("sdk-viewB", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "k7"),
		ref("sdk-viewB", KindFlag, "k7"),
		flagPut("k7", 7),
		cursor("sdk-viewA", "sel-a", 7),
		cursor("sdk-viewB", "sel-b", 7),
		refDelete("sdk-viewA", KindFlag, "k7"),
		refDelete("sdk-viewB", KindFlag, "k7"),
	)
	s.RemoveScope("sdk-viewA")
	s.DiscardState("sdk-viewB")
	apply(t, s, del(KindFlag, "k7", 8))

	s.mu.RLock()
	defer s.mu.RUnlock()
	assert.Empty(t, s.retracted)
	assert.Empty(t, s.scopes["sdk-viewB"].evicted)
}

func TestRefDeleteRemovesMembership(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f2"),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-viewA", "sel-a", 19),
		refDelete("sdk-viewA", KindFlag, "f2"),
	)

	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
	assert.ElementsMatch(t, []string{"sdk-viewA"}, s.ScopesFor(ObjectID{KindFlag, "f1"}))
	assert.Empty(t, s.ScopesFor(ObjectID{KindFlag, "f2"}),
		"the object stays in the base store but no scope references it")
}

// The payload version is the version at which a change applies, not the object's own, and a
// membership-driven re-entry may repeat it. Gating on it would drop legitimate changes.
func TestObjectMessagesApplyInStreamOrderWithoutGatingOnVersion(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		put(KindFlag, "f1", 19, `{"key":"f1","version":1,"on":true}`),
		// A repeated version, which happens when membership moved but content did not.
		put(KindFlag, "f1", 19, `{"key":"f1","version":1,"on":false}`),
	)
	record, ok := s.Get(ObjectID{KindFlag, "f1"})
	require.True(t, ok)
	assert.JSONEq(t, `{"key":"f1","version":1,"on":false}`, string(record.Bytes()))

	// Even a lower version applies: the stream is the ordering authority.
	apply(t, s, put(KindFlag, "f1", 5, `{"key":"f1","version":1,"on":true}`))
	record, ok = s.Get(ObjectID{KindFlag, "f1"})
	require.True(t, ok)
	assert.JSONEq(t, `{"key":"f1","version":1,"on":true}`, string(record.Bytes()))
}

// The row goes entirely on an eviction. A tombstone would exist to lose a version
// comparison, and this store makes none -- so it would be a row nothing reads that nothing
// ever reclaims.
func TestAnEvictionRemovesTheRowEntirely(t *testing.T) {
	s := newTestStore(t)
	apply(t, s, intent("sdk-env", wire.IntentTransferFull, true), flagPut("f1", 19),
		del(KindFlag, "f1", 20))

	_, ok := s.Get(ObjectID{KindFlag, "f1"})
	assert.False(t, ok, "the row goes entirely rather than leaving a tombstone")
	assert.Empty(t, s.Members("sdk-env"))
}

// A repeated full transfer for a scope that already holds membership is a resync: rebuild
// from the delivery and keep nothing it does not re-assert.
func TestAResyncRebuildsMembershipAndSwapsAtTheCursor(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f2"),
		flagPut("f1", 19), flagPut("f2", 19), flagPut("f3", 19),
		cursor("sdk-viewA", "sel-a", 19),
	)
	require.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-viewA"))

	// The resync starts and re-asserts a different set.
	apply(t, s, intent("sdk-viewA", wire.IntentTransferFull, false))
	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-viewA"),
		"the old membership is still served while the replacement is built")

	apply(t, s, ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f3"))
	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-viewA"),
		"a half-built membership is never exposed")

	apply(t, s, cursor("sdk-viewA", "sel-a2", 20))
	assert.ElementsMatch(t, ids("flag/f1", "flag/f3"), s.Members("sdk-viewA"),
		"f2 was not re-asserted, so it is gone")

	// The reverse index followed the swap.
	assert.Empty(t, s.ScopesFor(ObjectID{KindFlag, "f2"}))
	assert.ElementsMatch(t, []string{"sdk-viewA"}, s.ScopesFor(ObjectID{KindFlag, "f3"}))

	state, _ := s.State("sdk-viewA")
	assert.Equal(t, "sel-a2", state)
}

// A full transfer builds its membership aside and swaps it in at the cursor, so an initial
// hydration is invisible until it completes. That costs nothing, because a scope is not
// servable before its first cursor anyway, and it means one rule covers both the first
// hydration and every later resync.
func TestAFirstHydrationIsInvisibleUntilItsCursor(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19),
	)

	assert.Empty(t, s.Members("sdk-viewA"))
	assert.Empty(t, s.Pending("sdk-viewA"))
	assert.Empty(t, s.ScopesFor(ObjectID{KindFlag, "f1"}),
		"a content change need not reach a scope that is not serving yet")
	_, hydrated := s.State("sdk-viewA")
	assert.False(t, hydrated)

	apply(t, s, cursor("sdk-viewA", "sel-a", 19))

	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
	assert.ElementsMatch(t, []string{"sdk-viewA"}, s.ScopesFor(ObjectID{KindFlag, "f1"}))
	_, hydrated = s.State("sdk-viewA")
	assert.True(t, hydrated)
}

// The pending set has to be accurate the moment a cursor is applied, because that is when a
// caller decides whether the selector is safe to present or persist.
func TestPendingIsAccurateWhenTheCursorLands(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		ref("sdk-viewA", KindFlag, "f2"),
		flagPut("f1", 19),
		// f2 never arrives, so its reference is outstanding at the cursor.
		cursor("sdk-viewA", "sel-a", 19),
	)

	assert.ElementsMatch(t, ids("flag/f2"), s.Pending("sdk-viewA"))
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable, "the caller must not present or persist this selector yet")
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// A changes transfer is the opposite case: its delivery is a delta and nothing is discarded.
func TestAChangesTransferPatchesMembershipInPlace(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f2"),
		flagPut("f1", 19), flagPut("f2", 19), flagPut("f3", 19),
		cursor("sdk-viewA", "sel-a", 19),
		// A resumed scope: only the changes follow.
		intent("sdk-viewA", wire.IntentTransferChanges, false),
		refDelete("sdk-viewA", KindFlag, "f2"),
		ref("sdk-viewA", KindFlag, "f3"),
		cursor("sdk-viewA", "sel-a2", 20),
	)

	assert.ElementsMatch(t, ids("flag/f1", "flag/f3"), s.Members("sdk-viewA"))
}

// The degenerate resume: nothing changed, so the delivery is the cursor alone.
func TestANoneIntentJustRestatesTheCursor(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), flagPut("f1", 19),
		cursor("sdk-viewA", "sel-a", 19),
		intent("sdk-viewA", wire.IntentNone, false),
		cursor("sdk-viewA", "sel-a", 19),
	)
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// Removal is wholesale: the server sends no per-object ref-deletes for it, and discarding the
// recorded state alongside the membership is what stops a later registration of the same
// credential from resuming a delta onto nothing.
func TestRemoveScopeDiscardsMembershipAndState(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19),
		cursor("sdk-env", "sel-env", 19),
		cursor("sdk-viewA", "sel-a", 19),
	)

	s.RemoveScope("sdk-viewA")

	assert.Nil(t, s.Members("sdk-viewA"))
	assert.Nil(t, s.Pending("sdk-viewA"))
	_, hydrated := s.State("sdk-viewA")
	assert.False(t, hydrated)
	assert.ElementsMatch(t, []string{"sdk-env"}, s.Scopes())

	// The object stays in the base store for the remaining scopes, and no longer counts the
	// removed one among its referents.
	assert.ElementsMatch(t, []string{"sdk-env"}, s.ScopesFor(ObjectID{KindFlag, "f1"}),
		"only the unfiltered scope remains, and it references everything implicitly")
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-env"))

	s.RemoveScope("never-registered")
}

// One content change reaches every scope referencing the object plus every unfiltered scope,
// which references everything without saying so.
func TestScopesForCoversReferencesAndUnfilteredScopes(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("mob-env", wire.IntentTransferFull, true),
		intent("sdk-viewA", wire.IntentTransferFull, false),
		intent("sdk-viewB", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		ref("sdk-viewB", KindFlag, "f1"),
		ref("sdk-viewB", KindFlag, "f2"),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-env", "sel-env", 19), cursor("mob-env", "sel-mob", 19),
		cursor("sdk-viewA", "sel-a", 19), cursor("sdk-viewB", "sel-b", 19),
	)

	assert.ElementsMatch(t, []string{"mob-env", "sdk-env", "sdk-viewA", "sdk-viewB"}, s.ScopesFor(ObjectID{KindFlag, "f1"}))
	assert.ElementsMatch(t, []string{"mob-env", "sdk-env", "sdk-viewB"}, s.ScopesFor(ObjectID{KindFlag, "f2"}))
	assert.ElementsMatch(t, []string{"mob-env", "sdk-env"}, s.ScopesFor(ObjectID{KindFlag, "not-in-the-store"}),
		"an unfiltered scope would see it the moment it arrived")
}

// An unfiltered scope may see everything the base store holds, which is not the same as
// being able to serve something the base store does not hold. Testing membership before
// content would answer this one wrong.
func TestServesRequiresContentEvenForAnUnfilteredScope(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		flagPut("f1", 19),
		cursor("sdk-env", "sel-env", 19),
	)

	assert.True(t, s.Serves("sdk-env", ObjectID{KindFlag, "f1"}))
	assert.False(t, s.Serves("sdk-env", ObjectID{KindFlag, "never-arrived"}),
		"there is nothing to serve, however wide the scope's visibility is")

	// An evicted object has nothing to serve.
	apply(t, s, del(KindFlag, "f1", 20))
	assert.False(t, s.Serves("sdk-env", ObjectID{KindFlag, "f1"}))
}

// Kind is an opaque discriminator. Relay is an intermediary, so dropping a kind it does not
// recognize would starve a newer SDK behind an older relay.
func TestUnknownKindsAreStoredAndServed(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", "ai-config", "c1"),
		put("ai-config", "c1", 19, `{"key":"c1","model":"something"}`),
		cursor("sdk-env", "sel-env", 19), cursor("sdk-viewA", "sel-a", 19),
	)

	assert.ElementsMatch(t, ids("ai-config/c1"), s.Members("sdk-viewA"))
	assert.ElementsMatch(t, ids("ai-config/c1"), s.Members("sdk-env"))

	record, ok := s.Get(ObjectID{"ai-config", "c1"})
	require.True(t, ok)
	assert.JSONEq(t, `{"key":"c1","model":"something"}`, string(record.Bytes()))

	item, err := record.Item()
	require.NoError(t, err)
	assert.Nil(t, item, "an unrecognized kind never parses, and that is not an error")
}

// An unfiltered scope receives no refs at all, so one arriving means this relay and the
// server disagree about whether the credential is filtered. That is not safe to paper over:
// if the refs are the truth then the declaration was wrong and the scope is being served the
// whole base store. The store reports it so the caller can reconnect.
func TestARefForAnUnfilteredScopeIsReported(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-env", "sel-env", 19),
	)

	err := s.Apply(ref("sdk-env", KindFlag, "f1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unfiltered")

	// The stray ref changed nothing: membership is still the whole base, and the scope has
	// not started carrying a membership set.
	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-env"))
	assert.Empty(t, s.Pending("sdk-env"))
}

// Scope-tagged messages for a scope the store never saw opened are dropped: the unfiltered
// declaration only arrives on the intent, so inventing the scope would be guessing at it.
func TestMessagesForAnUnopenedScopeAreIgnored(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		ref("sdk-unknown", KindFlag, "f1"),
		refDelete("sdk-unknown", KindFlag, "f1"),
		cursor("sdk-unknown", "sel-x", 19),
	)

	assert.Empty(t, s.Scopes())
	assert.Nil(t, s.Members("sdk-unknown"))
	_, hydrated := s.State("sdk-unknown")
	assert.False(t, hydrated)
}

// A scope that switches to unfiltered releases the membership it had, so it stops carrying
// per-object bookkeeping it no longer needs.
func TestAScopeBecomingUnfilteredReleasesItsMembership(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-key", wire.IntentTransferFull, false),
		ref("sdk-key", KindFlag, "f1"),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-key", "sel-1", 19),
	)
	require.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-key"))

	apply(t, s, intent("sdk-key", wire.IntentTransferFull, true), cursor("sdk-key", "sel-2", 20))

	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-key"))
	assert.Empty(t, s.Pending("sdk-key"))
	assert.ElementsMatch(t, []string{"sdk-key"}, s.ScopesFor(ObjectID{KindFlag, "f2"}))
}

func TestRefBatchAppliesRefsAndDeletes(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewB", wire.IntentTransferFull, false),
		wire.RefBatch{
			T: wire.TypeRefBatch, Scope: "sdk-viewB",
			Refs: []wire.ObjectRef{{Kind: KindFlag, Key: "f3"}, {Kind: KindSegment, Key: "s2"}},
		},
		flagPut("f3", 19), put(KindSegment, "s2", 19, `{"key":"s2","version":1}`),
		cursor("sdk-viewB", "sel-b", 19),
		wire.RefBatch{
			T: wire.TypeRefBatch, Scope: "sdk-viewB",
			Refs:    []wire.ObjectRef{{Kind: KindFlag, Key: "f4"}},
			Deletes: []wire.ObjectRef{{Kind: KindSegment, Key: "s2"}},
		},
		flagPut("f4", 20),
		cursor("sdk-viewB", "sel-b2", 20),
	)

	assert.ElementsMatch(t, ids("flag/f3", "flag/f4"), s.Members("sdk-viewB"))
}

func TestApplyRejectsMessagesItCannotUse(t *testing.T) {
	s := newTestStore(t)
	assert.Error(t, s.Apply(intent("", wire.IntentTransferFull, false)),
		"an intent with no scope names nothing to open")
	assert.Error(t, s.Apply(put(KindFlag, "", 19, `{}`)),
		"an object message with no key names nothing to store")
	assert.NoError(t, s.Apply(wire.Heartbeat{T: wire.TypeHeartbeat}),
		"a message this store has no work for is ignored")
	assert.NoError(t, s.Apply(wire.Unknown{T: "invented-later"}))
}

func TestRecordParsesFlagsAndSegmentsOnFirstNeed(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		put(KindFlag, "f1", 19, `{"key":"f1","version":7,"on":true,"variations":["a","b"]}`),
		put(KindSegment, "s1", 19, `{"key":"s1","version":3,"included":["user-0"]}`),
	)

	record, ok := s.Get(ObjectID{KindFlag, "f1"})
	require.True(t, ok)
	item, err := record.Item()
	require.NoError(t, err)
	flag, isFlag := item.(*ldmodel.FeatureFlag)
	require.True(t, isFlag)
	assert.Equal(t, "f1", flag.Key)
	assert.Equal(t, 7, flag.Version)

	// The parse is memoized per record, so the same pointer comes back.
	again, err := record.Item()
	require.NoError(t, err)
	assert.Same(t, item, again)

	record, ok = s.Get(ObjectID{KindSegment, "s1"})
	require.True(t, ok)
	item, err = record.Item()
	require.NoError(t, err)
	segment, isSegment := item.(*ldmodel.Segment)
	require.True(t, isSegment)
	assert.Equal(t, "s1", segment.Key)
}

// Valid JSON the model cannot parse is most likely a schema newer than this relay's. The
// error lets a parsed consumer treat the object as absent while byte-through surfaces keep
// forwarding the content they were given.
func TestRecordReportsContentTheModelCannotParse(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		put(KindFlag, "f1", 19, `{"key":"f1","version":"not-a-number"}`),
	)

	record, ok := s.Get(ObjectID{KindFlag, "f1"})
	require.True(t, ok)
	item, err := record.Item()
	assert.Error(t, err)
	assert.Nil(t, item)
	assert.NotEmpty(t, record.Bytes(), "the bytes are still there to forward")
}

// The store takes one writer and any number of readers. Apply runs on the goroutine that
// consumes the ordered stream; everything serving downstream reads concurrently with it.
func TestReadsAreSafeWhileApplyIsWriting(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		intent("sdk-viewA", wire.IntentTransferFull, false),
		intent("transient", wire.IntentTransferFull, false),
		cursor("sdk-env", "sel-env", 1),
		cursor("sdk-viewA", "sel-a", 1),
		cursor("transient", "sel-t", 1),
	)

	const updates = 500
	writing := make(chan struct{})
	go func() {
		defer close(writing)
		for i := range updates {
			key := "f" + string(rune('a'+i%8))
			// assert rather than require: this is a spawned goroutine, and require's
			// FailNow is documented as test-goroutine-only, degrading to a bare Goexit
			// that reports confusingly.
			assert.NoError(t, s.Apply(flagPut(key, i)))
			assert.NoError(t, s.Apply(ref("sdk-viewA", KindFlag, key)))
			if i%16 == 0 {
				assert.NoError(t, s.Apply(del(KindFlag, key, i)))
			}
			if i%64 == 0 {
				// RemoveScope is a second write path; exercise it against the readers too.
				s.RemoveScope("transient")
			}
		}
	}()

	readers := 4
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
				s.Members("sdk-env")
				s.Members("sdk-viewA")
				s.Pending("sdk-viewA")
				s.ScopesFor(ObjectID{KindFlag, "fa"})
				s.State("sdk-viewA")
				if record, ok := s.Get(ObjectID{KindFlag, "fa"}); ok {
					_, _ = record.Item()
				}
			}
		}()
	}

	<-writing
	for range readers {
		<-done
	}
}

// --- Regressions from the multi-agent review of 2026-09-25 ---

// A delivery's rebuild buffer must not outlive the delivery. When one is interrupted -- a
// disconnect, or a bad frame that aborts mid-stream -- the next delivery's cursor would
// otherwise install the abandoned half-built set as the scope's whole membership.
func TestAnAbandonedRebuildDoesNotCaptureTheNextDelivery(t *testing.T) {
	for name, resume := range map[string]string{
		// A delta delivery adds to the held membership and discards nothing.
		"resumed with changes": wire.IntentTransferChanges,
		// The degenerate resume: nothing changed, so the delivery is the cursor alone. This
		// is the worse variant, because it needs no refs to blank the scope.
		"resumed with nothing": wire.IntentNone,
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			apply(t, s,
				intent("sdk-viewA", wire.IntentTransferFull, false),
				ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f2"),
				ref("sdk-viewA", KindFlag, "f3"),
				flagPut("f1", 19), flagPut("f2", 19), flagPut("f3", 19), flagPut("f4", 19),
				cursor("sdk-viewA", "sel-1", 19),
			)
			require.ElementsMatch(t, ids("flag/f1", "flag/f2", "flag/f3"), s.Members("sdk-viewA"))

			// A resync opens and is interrupted before its cursor.
			apply(t, s,
				intent("sdk-viewA", wire.IntentTransferFull, false),
				ref("sdk-viewA", KindFlag, "f1"),
			)

			// The connection ends, and the next one resumes from the recorded selector.
			apply(t, s, intent("sdk-viewA", resume, false))
			if resume == wire.IntentTransferChanges {
				apply(t, s, ref("sdk-viewA", KindFlag, "f4"))
			}
			apply(t, s, cursor("sdk-viewA", "sel-2", 20))

			switch resume {
			case wire.IntentTransferChanges:
				assert.ElementsMatch(t,
					ids("flag/f1", "flag/f2", "flag/f3", "flag/f4"), s.Members("sdk-viewA"),
					"a delta adds to the held membership and discards nothing")
			case wire.IntentNone:
				assert.ElementsMatch(t,
					ids("flag/f1", "flag/f2", "flag/f3"), s.Members("sdk-viewA"),
					"nothing changed, so the membership is untouched")
			}
		})
	}
}

// A scope whose assignment widens to environment-wide must not carry a membership set out of
// the delivery that was interrupted by the change.
func TestAnUnfilteredFlipDoesNotInheritAHalfBuiltMembership(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-key", wire.IntentTransferFull, false),
		ref("sdk-key", KindFlag, "f1"),
		flagPut("f1", 19), flagPut("f2", 19),
		// The credential's last view is unlinked, so it re-opens environment-wide before the
		// first delivery ever completed.
		intent("sdk-key", wire.IntentTransferFull, true),
		cursor("sdk-key", "sel-1", 20),
	)

	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-key"))
	assert.Empty(t, s.Pending("sdk-key"),
		"an unfiltered scope holds no references, so nothing of its can be pending")
	_, presentable := s.State("sdk-key")
	assert.True(t, presentable, "an unfiltered scope's selector is never gated by a stale ref")

	// An ordinary eviction later must not arm pending through a stale reverse-index row,
	// which would block this scope's selector for the rest of the session. The eviction
	// withholds the selector only until the scope's next cursor.
	apply(t, s, del(KindFlag, "f1", 21), cursor("sdk-key", "sel-2", 21))
	assert.Empty(t, s.Pending("sdk-key"))
	_, presentable = s.State("sdk-key")
	assert.True(t, presentable)
	assert.Equal(t, []string{"sdk-key"}, s.ScopesFor(ObjectID{KindFlag, "f1"}),
		"reached as an unfiltered scope, not through a leftover reference")
}

// A retraction inside a full transfer applies to the set being built, not the one being
// served -- otherwise a delivery later abandoned would have permanently narrowed the scope.
func TestARetractionDuringARebuildLeavesTheLiveMembershipAlone(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f2"),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-viewA", "sel-1", 19),
	)

	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f2"),
		refDelete("sdk-viewA", KindFlag, "f2"),
	)
	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-viewA"),
		"the pre-resync membership still holds until the cursor")
	assert.True(t, s.Serves("sdk-viewA", ObjectID{KindFlag, "f2"}))

	apply(t, s, cursor("sdk-viewA", "sel-2", 20))
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// Nothing is served before a scope's first cursor, which is its hydration-complete signal.
// An unfiltered scope would otherwise serve the whole base store from its intent alone.
func TestAScopeServesNothingBeforeItsFirstCursor(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		flagPut("f1", 19),
		cursor("sdk-env", "sel-env", 19),
		// A second credential registers; the base store is already populated.
		intent("mob-env", wire.IntentTransferFull, true),
	)

	assert.Empty(t, s.Members("mob-env"))
	assert.False(t, s.Serves("mob-env", ObjectID{KindFlag, "f1"}))
	_, presentable := s.State("mob-env")
	assert.False(t, presentable)

	apply(t, s, cursor("mob-env", "sel-mob", 19))
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("mob-env"))
}

// An absent required field is not the open-set tolerance the protocol asks for. Admitting one
// would enter a membership entry no object message could ever address, leaving the scope's
// selector permanently ungateable.
func TestApplyRejectsMessagesMissingRequiredFields(t *testing.T) {
	s := newTestStore(t)
	apply(t, s, intent("sdk-viewA", wire.IntentTransferFull, false))

	for name, msg := range map[string]any{
		"intent with no scope":       intent("", wire.IntentTransferFull, false),
		"intent with no declaration": wire.ServerIntent{T: wire.TypeServerIntent, Scope: "sdk-viewA", IntentCode: wire.IntentTransferChanges},
		"put with no key":            put(KindFlag, "", 19, `{}`),
		"put with no kind":           put("", "f1", 19, `{}`),
		"delete with no key":         del(KindFlag, "", 19),
		"delete with no kind":        del("", "f1", 19),
		"ref with no kind or key":    ref("sdk-viewA", "", ""),
		"ref with no key":            ref("sdk-viewA", KindFlag, ""),
		"ref with no scope":          ref("", KindFlag, "f1"),
		"ref-delete with no key":     refDelete("sdk-viewA", KindFlag, ""),
		"cursor with no scope":       cursor("", "sel-1", 19),
		"cursor with no state":       cursor("sdk-viewA", "", 19),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, s.Apply(msg))
		})
	}

	// None of the rejected messages left anything behind.
	assert.Empty(t, s.Pending("sdk-viewA"))
	assert.Empty(t, s.Members("sdk-viewA"))
}

// The schema closes the intent codes to three values, so an unrecognized one is a contract
// violation rather than a code to guess at. Guessing is unsafe in both directions: read as a
// delta it would retain membership a rebuild meant to discard, and read as a rebuild it would
// blank a hydrated scope and then record a presentable selector for it.
func TestAnUnrecognizedIntentCodeIsRejected(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19),
		cursor("sdk-viewA", "sel-1", 19),
	)

	err := s.Apply(intent("sdk-viewA", "xfer-invented-later", false))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized code")

	// The scope is untouched: still serving, still holding a presentable selector.
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
	state, presentable := s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-1", state)
}

// A refusal means the server could not decide, not that the credential is bad: the membership
// is retained so that key's downstream keeps being served, and only the selector goes.
func TestDiscardStateKeepsMembership(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19),
		cursor("sdk-viewA", "sel-1", 19),
	)

	s.DiscardState("sdk-viewA")

	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable, "with no state to present, the next registration is a full hydration")
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"),
		"the membership is retained, so the credential's SDKs keep being served")

	s.DiscardState("never-registered")
}

// An eviction reclaims the row, so the store grows in the number of objects held rather than
// the number ever seen.
func TestEvictionsDoNotAccumulateRows(t *testing.T) {
	s := newTestStore(t)
	apply(t, s, intent("sdk-env", wire.IntentTransferFull, true), cursor("sdk-env", "sel-1", 1))

	for i := range 200 {
		key := "churn"
		apply(t, s, flagPut(key, i), del(KindFlag, key, i))
	}

	assert.Empty(t, s.Members("sdk-env"))
	_, held := s.Get(ObjectID{KindFlag, "churn"})
	assert.False(t, held)
}

// A resume answers a state this relay presented, and its delivery carries only what changed
// since it. With no membership to apply that delta to, the rest would never arrive and the
// scope would look complete while serving a fraction of its objects. The protocol discards a
// recorded state alongside its membership so this pairing cannot occur, so meeting it means
// something upstream is inconsistent.
func TestAResumeForAScopeWithNoMembershipIsRejected(t *testing.T) {
	s := newTestStore(t)

	for _, code := range []string{wire.IntentTransferChanges, wire.IntentNone} {
		err := s.Apply(intent("sdk-view", code, false))
		require.Error(t, err, code)
		assert.Contains(t, err.Error(), "presented no state")
	}

	// Nothing was created, so the delivery that would have followed is dropped rather than
	// building a membership with no basis.
	assert.Empty(t, s.Scopes())
	apply(t, s, ref("sdk-view", KindFlag, "f7"), flagPut("f7", 20))
	assert.Nil(t, s.Members("sdk-view"))

	// A full hydration for the same credential is accepted, which is the sound answer.
	apply(t, s,
		intent("sdk-view", wire.IntentTransferFull, false),
		ref("sdk-view", KindFlag, "f7"),
		cursor("sdk-view", "sel-1", 20),
	)
	assert.ElementsMatch(t, ids("flag/f7"), s.Members("sdk-view"))
}

// Content is required on an object message. Accepting one without it used to clear the
// pending entry for every referencing scope while holding nothing, leaving a member that was
// neither servable nor pending -- and therefore a presentable selector for a state whose
// content this relay did not have, which is the one thing the selector rule exists to stop.
func TestAPutWithNoContentIsRejected(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		cursor("sdk-viewA", "sel-1", 19),
	)
	require.ElementsMatch(t, ids("flag/f1"), s.Pending("sdk-viewA"))

	err := s.Apply(wire.PutObject{
		T: wire.TypePutObject, Base: base, Kind: KindFlag, Key: "f1",
		Version: 20,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no content")

	// An empty value is no content either. The msgpack decoder yields a non-nil empty slice
	// for an empty binary value, so a nil check alone would admit it.
	err = s.Apply(wire.PutObject{
		T: wire.TypePutObject, Base: base, Kind: KindFlag, Key: "f1",
		Version: 20, Object: []byte{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no content")

	// The reference is still pending, so the selector stays withheld rather than becoming
	// presentable for content the store does not hold.
	assert.ElementsMatch(t, ids("flag/f1"), s.Pending("sdk-viewA"))
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable)
	assert.Empty(t, s.Members("sdk-viewA"))
}

// A batch applies whole or not at all. Holding the write lock hides a partial batch from
// readers but does not undo it, and a caller told a rejected message changed nothing has to
// be able to rely on that.
func TestARejectedRefBatchAppliesNothing(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-viewA", "sel-1", 19),
	)

	err := s.Apply(wire.RefBatch{
		T: wire.TypeRefBatch, Scope: "sdk-viewA",
		Refs: []wire.ObjectRef{
			{Kind: KindFlag, Key: "f2"},
			{Kind: KindFlag, Key: ""}, // rejected, so nothing in the batch applies
		},
		Deletes: []wire.ObjectRef{{Kind: KindFlag, Key: "f1"}},
	})
	require.Error(t, err)

	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"),
		"f2 was not added and f1 was not removed")

	// The same holds when the bad entry is in the deletes.
	err = s.Apply(wire.RefBatch{
		T: wire.TypeRefBatch, Scope: "sdk-viewA",
		Refs:    []wire.ObjectRef{{Kind: KindFlag, Key: "f2"}},
		Deletes: []wire.ObjectRef{{Kind: KindFlag, Key: ""}},
	})
	require.Error(t, err)
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// The retraction path refuses the same relay-and-server disagreement the reference path does.
func TestARefDeleteForAnUnfilteredScopeIsReported(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-env", wire.IntentTransferFull, true),
		flagPut("f1", 19),
		cursor("sdk-env", "sel-env", 19),
	)

	err := s.Apply(refDelete("sdk-env", KindFlag, "f1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unfiltered")
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-env"))
}

// Hydration is readable on its own, because a scope can be hydrated with no selector to
// present: a refusal discards the selector while the membership keeps being served, and the
// sweep and the pending-ref grace period are both defined against the cursor having arrived.
func TestHydratedIsSeparateFromHavingASelector(t *testing.T) {
	s := newTestStore(t)
	apply(t, s, intent("sdk-viewA", wire.IntentTransferFull, false))
	assert.False(t, s.Hydrated("sdk-viewA"))
	assert.False(t, s.Hydrated("never-registered"))

	apply(t, s,
		ref("sdk-viewA", KindFlag, "f1"), flagPut("f1", 19),
		cursor("sdk-viewA", "sel-1", 19),
	)
	assert.True(t, s.Hydrated("sdk-viewA"))

	s.DiscardState("sdk-viewA")
	assert.True(t, s.Hydrated("sdk-viewA"), "the servable latch survives a discarded selector")
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable)
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
}

// A key that gains its first view becomes filtered through a full transfer. Until that
// delivery's cursor the scope keeps serving the whole base store under its old selector, so
// the selector it presents always describes the membership it serves.
func TestAScopeBecomingFilteredKeepsServingUntilItsCursor(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-key", wire.IntentTransferFull, true),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-key", "sel-1", 19),
	)

	apply(t, s,
		intent("sdk-key", wire.IntentTransferFull, false),
		ref("sdk-key", KindFlag, "f1"),
	)
	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-key"),
		"the unfiltered membership holds until the cursor")
	assert.True(t, s.Serves("sdk-key", ObjectID{KindFlag, "f2"}))
	state, presentable := s.State("sdk-key")
	assert.True(t, presentable)
	assert.Equal(t, "sel-1", state)

	apply(t, s, cursor("sdk-key", "sel-2", 20))
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-key"))
	assert.False(t, s.Serves("sdk-key", ObjectID{KindFlag, "f2"}))
	assert.Empty(t, s.ScopesFor(ObjectID{KindFlag, "f2"}))
	state, presentable = s.State("sdk-key")
	assert.True(t, presentable)
	assert.Equal(t, "sel-2", state)
}

// A delivery that would make a scope filtered and is then interrupted leaves the scope as it
// was. The next delivery's declaration is the one that applies.
func TestAnInterruptedChangeOfDeclarationLeavesTheScopeAsItWas(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-key", wire.IntentTransferFull, true),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-key", "sel-1", 19),
		intent("sdk-key", wire.IntentTransferFull, false),
		ref("sdk-key", KindFlag, "f1"),
		// The view is unlinked again before the delivery completes.
		intent("sdk-key", wire.IntentTransferFull, true),
		cursor("sdk-key", "sel-2", 20),
	)

	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-key"))
	assert.Empty(t, s.Pending("sdk-key"))
}

// The other direction is staged the same way: a scope whose last view is unlinked keeps its
// membership until the cursor, and the delivery that widens it accepts no refs.
func TestAScopeBecomingUnfilteredKeepsItsMembershipUntilItsCursor(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-key", wire.IntentTransferFull, false),
		ref("sdk-key", KindFlag, "f1"),
		flagPut("f1", 19), flagPut("f2", 19),
		cursor("sdk-key", "sel-1", 19),
		intent("sdk-key", wire.IntentTransferFull, true),
	)

	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-key"))
	err := s.Apply(ref("sdk-key", KindFlag, "f2"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unfiltered")

	apply(t, s, cursor("sdk-key", "sel-2", 20))
	assert.ElementsMatch(t, ids("flag/f1", "flag/f2"), s.Members("sdk-key"))
}

// A delta is relative to the membership the presented state describes, and an unfiltered
// scope holds none. A resume that changes the declaration is rejected and changes no
// membership. The selector is discarded, so the reconnect does not present it again.
func TestAResumeThatChangesTheDeclarationIsRejected(t *testing.T) {
	for name, from := range map[string]bool{"to filtered": true, "to unfiltered": false} {
		t.Run(name, func(t *testing.T) {
			for _, code := range []string{wire.IntentTransferChanges, wire.IntentNone} {
				s := newTestStore(t)
				apply(t, s, intent("sdk-key", wire.IntentTransferFull, from))
				if !from {
					apply(t, s, ref("sdk-key", KindFlag, "f1"))
				}
				apply(t, s,
					flagPut("f1", 19), flagPut("f2", 19),
					cursor("sdk-key", "sel-1", 19),
				)
				before := s.Members("sdk-key")

				err := s.Apply(intent("sdk-key", code, !from))
				require.Error(t, err, code)
				assert.Contains(t, err.Error(), "changed its unfiltered declaration")

				assert.ElementsMatch(t, before, s.Members("sdk-key"))
				_, presentable := s.State("sdk-key")
				assert.False(t, presentable, "the next handshake hydrates in full")

				apply(t, s,
					intent("sdk-key", wire.IntentTransferFull, !from),
					cursor("sdk-key", "sel-2", 20),
				)
				state, presentable := s.State("sdk-key")
				assert.True(t, presentable)
				assert.Equal(t, "sel-2", state)
			}
		})
	}
}

// A relay presents no state for a scope that never completed its first delivery, or whose
// selector a refusal discarded. A resume for either has nothing to apply its delta to.
func TestAResumeWithNoPresentableStateIsRejected(t *testing.T) {
	t.Run("never hydrated", func(t *testing.T) {
		s := newTestStore(t)
		apply(t, s,
			intent("sdk-view", wire.IntentTransferFull, false),
			ref("sdk-view", KindFlag, "f1"),
		)
		err := s.Apply(intent("sdk-view", wire.IntentTransferChanges, false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "presented no state")
		assert.False(t, s.Hydrated("sdk-view"))
	})

	t.Run("state discarded", func(t *testing.T) {
		s := newTestStore(t)
		apply(t, s,
			intent("sdk-view", wire.IntentTransferFull, false),
			ref("sdk-view", KindFlag, "f1"), flagPut("f1", 19),
			cursor("sdk-view", "sel-1", 19),
		)
		s.DiscardState("sdk-view")
		err := s.Apply(intent("sdk-view", wire.IntentNone, false))
		require.Error(t, err)
		assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-view"),
			"the retained membership keeps being served")
	})
}

// An older selector is never offered while a reference is pending. A resume carries the net
// changes since the presented state, and the store applies later messages in place, so a
// change made and undone after that state would never be re-sent.
func TestStateOffersNoOlderSelectorWhileAReferenceIsPending(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "y"), flagPut("y", 19),
		cursor("sdk-viewA", "sel-1", 19),
		ref("sdk-viewA", KindFlag, "z"),
		refDelete("sdk-viewA", KindFlag, "y"),
		cursor("sdk-viewA", "sel-2", 20),
	)
	_, presentable := s.State("sdk-viewA")
	assert.False(t, presentable)

	apply(t, s, flagPut("z", 20))
	state, presentable := s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-2", state)
}

// A scope serves nothing before its first cursor, so a change of declaration before then
// applies at once. Staging it would route the new delivery's fan-out by the old one.
func TestADeclarationChangeBeforeTheFirstCursorAppliesAtOnce(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-view", wire.IntentTransferFull, true),
		intent("sdk-view", wire.IntentTransferFull, false),
	)
	assert.Empty(t, s.ScopesFor(ObjectID{KindFlag, "f1"}))

	s = newTestStore(t)
	apply(t, s,
		intent("sdk-view", wire.IntentTransferFull, false),
		ref("sdk-view", KindFlag, "f1"),
		intent("sdk-view", wire.IntentTransferFull, true),
	)
	assert.Equal(t, []string{"sdk-view"}, s.ScopesFor(ObjectID{KindFlag, "f2"}))
}

// A rejected resume still ends the delivery before it, so nothing that delivery built can
// be installed by a later cursor.
func TestARejectedResumeAbandonsTheEarlierDelivery(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-view", wire.IntentTransferFull, false),
		ref("sdk-view", KindFlag, "w"), flagPut("w", 19), flagPut("y", 19),
		cursor("sdk-view", "sel-1", 19),
		intent("sdk-view", wire.IntentTransferFull, false),
		ref("sdk-view", KindFlag, "y"),
	)
	require.Error(t, s.Apply(intent("sdk-view", wire.IntentTransferChanges, true)))

	apply(t, s, cursor("sdk-view", "sel-2", 20))
	assert.ElementsMatch(t, ids("flag/w"), s.Members("sdk-view"),
		"the abandoned rebuild was not installed")
}

// Whether a relay presented a state is decided at the handshake. Base-scope traffic may
// arrive before a scope's intent, and an eviction there makes a presented scope pending
// again. The resume is still valid, and its delta resolves the reference.
func TestAResumeAfterAnEvictionBeforeItsIntentIsAccepted(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-viewA", wire.IntentTransferFull, false),
		ref("sdk-viewA", KindFlag, "f1"), ref("sdk-viewA", KindFlag, "f9"),
		flagPut("f1", 19), flagPut("f9", 19),
		cursor("sdk-viewA", "sel-1", 19),
	)
	_, presentable := s.State("sdk-viewA")
	require.True(t, presentable, "the relay presents sel-1 at the handshake")

	apply(t, s,
		// f9 left the view while the relay was disconnected, and the base delta arrives
		// before this scope's answer.
		del(KindFlag, "f9", 20),
		intent("sdk-viewA", wire.IntentTransferChanges, false),
		refDelete("sdk-viewA", KindFlag, "f9"),
		cursor("sdk-viewA", "sel-2", 20),
	)
	assert.ElementsMatch(t, ids("flag/f1"), s.Members("sdk-viewA"))
	state, presentable := s.State("sdk-viewA")
	assert.True(t, presentable)
	assert.Equal(t, "sel-2", state)
}

// Content is spliced into larger JSON documents downstream, so content that is not JSON is
// rejected rather than corrupting every document that includes it.
func TestAPutWithContentThatIsNotJSONIsRejected(t *testing.T) {
	s := newTestStore(t)
	for _, body := range []string{`{"key":`, "{\"key\":\"\xff\"}"} {
		err := s.Apply(put(KindFlag, "f1", 19, body))
		require.Error(t, err, body)
		assert.Contains(t, err.Error(), "not JSON")
	}
	_, held := s.Get(ObjectID{KindFlag, "f1"})
	assert.False(t, held)
}

// A scope name is a credential, so errors carry only enough of it to identify the scope.
func TestErrorsMaskTheScopeCredential(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("sdk-0123-secret-wxyz", wire.IntentTransferFull, true),
		cursor("sdk-0123-secret-wxyz", "sel-1", 19),
	)
	err := s.Apply(ref("sdk-0123-secret-wxyz", KindFlag, "f1"))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "...wxyz")
}

// An unfiltered scope holds no references, so an eviction cannot make it pending. Its
// selector still claims the evicted object, and a resume from it would never re-send the
// object, so the selector is withheld until the scope's next cursor.
func TestAnEvictionWithholdsAnUnfilteredScopesSelectorUntilItsCursor(t *testing.T) {
	t.Run("scope not yet answered in this session", func(t *testing.T) {
		s := newTestStore(t)
		apply(t, s,
			intent("mob-env", wire.IntentTransferFull, true),
			flagPut("k1", 19), flagPut("k3", 19),
			cursor("mob-env", "sel-1", 19),
			// The scope is presented but not yet verified, so k3 leaves the union.
			del(KindFlag, "k3", 20),
		)
		_, presentable := s.State("mob-env")
		assert.False(t, presentable)

		apply(t, s,
			intent("mob-env", wire.IntentTransferChanges, true),
			flagPut("k3", 20),
			cursor("mob-env", "sel-2", 20),
		)
		state, presentable := s.State("mob-env")
		assert.True(t, presentable)
		assert.Equal(t, "sel-2", state)
		assert.ElementsMatch(t, ids("flag/k1", "flag/k3"), s.Members("mob-env"))
	})

	t.Run("during a staged change to filtered", func(t *testing.T) {
		s := newTestStore(t)
		apply(t, s,
			intent("mob-env", wire.IntentTransferFull, true),
			flagPut("k1", 19), flagPut("k2", 19),
			cursor("mob-env", "sel-1", 19),
			intent("mob-env", wire.IntentTransferFull, false),
			ref("mob-env", KindFlag, "k1"),
			del(KindFlag, "k2", 20),
		)
		_, presentable := s.State("mob-env")
		assert.False(t, presentable, "sel-1 claims k2, which the store no longer holds")
	})
}

// Whitespace between JSON tokens is legal content, including a carriage return, and the
// store keeps the bytes as they arrived. A surface that writes lines must handle it.
func TestAPutWithACarriageReturnIsStoredVerbatim(t *testing.T) {
	s := newTestStore(t)
	body := "{\"key\":\"f1\",\r\n\"version\":1}"
	apply(t, s, put(KindFlag, "f1", 19, body))
	record, held := s.Get(ObjectID{KindFlag, "f1"})
	require.True(t, held)
	assert.Equal(t, body, string(record.Bytes()))
}

// The selector goes back to the server and downstream as text, so it must be UTF-8.
func TestACursorWithAStateThatIsNotUTF8IsRejected(t *testing.T) {
	s := newTestStore(t)
	apply(t, s, intent("sdk-env", wire.IntentTransferFull, true))
	err := s.Apply(cursor("sdk-env", "sel-\xff", 19))
	require.Error(t, err)
	assert.False(t, s.Hydrated("sdk-env"))
}

// An eviction withholds an unfiltered scope's selector only while the object is missing.
// Fresh content restores what the selector claims, so the scope keeps its resume.
func TestFreshContentRestoresAnUnfilteredScopesSelector(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("mob-env", wire.IntentTransferFull, true),
		flagPut("k1", 19), flagPut("k2", 19), flagPut("k3", 19),
		cursor("mob-env", "sel-1", 19),
		del(KindFlag, "k2", 20), del(KindFlag, "k3", 20),
		flagPut("k2", 19),
	)
	_, presentable := s.State("mob-env")
	assert.False(t, presentable, "k3 is still missing")

	// Each eviction is remembered, not only the most recent one.
	apply(t, s, del(KindFlag, "k2", 21), flagPut("k3", 19))
	_, presentable = s.State("mob-env")
	assert.False(t, presentable, "k2 is missing again")

	apply(t, s, flagPut("k2", 19))
	state, presentable := s.State("mob-env")
	assert.True(t, presentable)
	assert.Equal(t, "sel-1", state)
}

// A delete for an object the store does not hold is a no-op, so it withholds nothing.
func TestADeleteForAnUnheldObjectLeavesAnUnfilteredScopePresentable(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("mob-env", wire.IntentTransferFull, true),
		flagPut("k1", 19),
		cursor("mob-env", "sel-1", 19),
		del(KindFlag, "never-held", 20),
	)
	_, presentable := s.State("mob-env")
	assert.True(t, presentable)
}

// Downstream surfaces write object identities into JSON, so an identity must be valid UTF-8.
func TestAnIdentityThatIsNotUTF8IsRejected(t *testing.T) {
	s := newTestStore(t)
	apply(t, s, intent("sdk-view", wire.IntentTransferFull, false))
	for name, msg := range map[string]any{
		"put":        put(KindFlag, "f\xff", 19, `{}`),
		"delete":     del(KindFlag, "f\xff", 19),
		"ref":        ref("sdk-view", KindFlag, "f\xff"),
		"ref-delete": refDelete("sdk-view", "fl\xffag", "f1"),
		"ref-batch": wire.RefBatch{
			T: wire.TypeRefBatch, Scope: "sdk-view",
			Refs: []wire.ObjectRef{{Kind: KindFlag, Key: "f\xff"}},
		},
	} {
		assert.Error(t, s.Apply(msg), name)
	}
}

// An object that arrives after an unfiltered scope's cursor is not claimed by its selector,
// so evicting it again before the next cursor withholds nothing. A filtered scope behaves
// the same way through its references.
func TestAnObjectCreatedAndEvictedSinceTheCursorLeavesTheSelectorPresentable(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("mob-env", wire.IntentTransferFull, true),
		flagPut("k0", 19),
		cursor("mob-env", "sel-1", 19),
		flagPut("k1", 20),
		del(KindFlag, "k1", 21),
	)
	state, presentable := s.State("mob-env")
	assert.True(t, presentable)
	assert.Equal(t, "sel-1", state)

	// Evicting an object the selector does claim still withholds it.
	apply(t, s, del(KindFlag, "k0", 21))
	_, presentable = s.State("mob-env")
	assert.False(t, presentable)

	// A later cursor claims what the store then holds, so an object created before it is
	// claimed from then on.
	apply(t, s,
		flagPut("k0", 22), flagPut("k1", 22),
		cursor("mob-env", "sel-2", 22),
		del(KindFlag, "k1", 23),
	)
	_, presentable = s.State("mob-env")
	assert.False(t, presentable, "sel-2 claims k1")
}

// A scope with no selector has nothing to protect, so it records no evictions. Its next
// cursor records a selector that covers only what the store then holds.
func TestAScopeWithNoStateRecordsNoEvictions(t *testing.T) {
	s := newTestStore(t)
	apply(t, s,
		intent("mob-env", wire.IntentTransferFull, true),
		flagPut("k0", 19), flagPut("k1", 19),
		cursor("mob-env", "sel-1", 19),
	)
	s.DiscardState("mob-env")
	apply(t, s, del(KindFlag, "k0", 20))

	s.mu.RLock()
	evicted := len(s.scopes["mob-env"].evicted)
	s.mu.RUnlock()
	assert.Zero(t, evicted)
}
