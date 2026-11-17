package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"unicode/utf8"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
)

// Store holds the base objects and the per-scope membership over them.
//
// There is one writer. The wire is a single ordered stream, so Apply runs on one goroutine
// and write contention does not arise by construction. Reads take the lock and are safe from
// any goroutine.
type Store struct {
	logger *slog.Logger

	mu      sync.RWMutex
	objects map[ObjectID]*Record
	scopes  map[string]*scope

	// referencing is the reverse of the scopes' membership sets: which scopes reference an
	// object. One content change resolves to its interested scopes through this rather than
	// by scanning every scope.
	referencing map[ObjectID]map[string]struct{}

	// unfiltered indexes the scopes currently served as unfiltered. Every content change
	// concerns them, and there are few of them, so walking this keeps a put or a delete from
	// costing a pass over every scope.
	unfiltered map[string]*scope
}

// scope is one registered credential's view of the base store.
type scope struct {
	// unfiltered scopes may see every object in the base store. They receive no refs, so
	// they carry no membership set: materializing one would cost per-object bookkeeping for
	// the commonest configuration, a plain environment key with no views.
	//
	// This is the declaration the scope is served under. A full transfer that changes it
	// takes effect at that delivery's cursor, together with the membership it builds.
	unfiltered bool

	// declaredUnfiltered is the declaration of the most recent intent, which governs the
	// delivery in flight. It differs from unfiltered only while a full transfer that changes
	// the declaration is in flight. Applying the change at the intent instead would blank a
	// scope that becomes filtered until its cursor, while its old selector stayed
	// presentable for a membership the store had already discarded.
	declaredUnfiltered bool

	// members is the scope's current membership, empty for an unfiltered scope.
	members map[ObjectID]struct{}

	// rebuilding is the membership a filtered full transfer is building. Refs land here and
	// replace members at the delivery's cursor, so a resync never exposes a half-built
	// membership. It is nil for an unfiltered full transfer and for a resume, and the next
	// intent discards it if the delivery never completes.
	rebuilding map[ObjectID]struct{}

	// pending is the subset of members whose content the store does not hold. A ref asserts
	// membership; the base store is the source of truth for existence, so a reference to an
	// object not held is a normal transient rather than an error.
	pending map[ObjectID]struct{}

	// state is the selector from the most recent applied cursor.
	state string

	// evicted is the set of objects an unfiltered scope's selector claims that the store lost
	// after that selector and does not hold again. A resume from the selector would never
	// re-send such an object. The server also counts it as held when it hydrates another
	// scope, and sends that scope a reference with no content. Fresh content for the object
	// removes it from the set, and the scope's next cursor clears the set, because the server
	// sends that cursor only after the content it covers. A filtered scope needs no such set:
	// an eviction makes its reference pending, and a ref-delete is a membership change, which
	// withholds the selector until the next cursor on its own.
	evicted map[ObjectID]struct{}

	// retracted and added are the objects a filtered scope dropped from and added to its live
	// membership after its last cursor. created is the objects that arrived after an
	// unfiltered scope's last cursor and that the store did not hold at that cursor. Together
	// they record how the scope's membership differs from what its selector names. A change
	// that is undone before the next cursor leaves the sets, so they hold the net difference.
	retracted map[ObjectID]struct{}
	added     map[ObjectID]struct{}
	created   map[ObjectID]struct{}

	// hydrated latches on the scope's first cursor and is cleared only by destroying the
	// scope. It is what makes the scope servable, and it is deliberately separate from
	// whether there is a selector to present: a refusal discards the selector while the
	// membership keeps being served, and nothing transient un-serves a scope that has
	// hydrated once.
	hydrated bool
}

// New builds an empty store.
func New(logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		logger:      logger.With("component", "MegaStreamStore"),
		objects:     map[ObjectID]*Record{},
		scopes:      map[string]*scope{},
		referencing: map[ObjectID]map[string]struct{}{},
		unfiltered:  map[string]*scope{},
	}
}

// Apply applies one decoded message.
//
// Messages apply in arrival order and never gate on version. The payload version on an
// object message is the version at which the change applies, not the object's own version,
// and a membership-driven departure or re-entry may repeat it -- so comparing versions here
// would drop legitimate changes. The single ordered stream is the ordering authority.
//
// A message this store has no work for is ignored. That includes the scope-tagged errors
// that destroy a registration: they arrive on the same ordered stream, and acting on them is
// the caller's, through RemoveScope or DiscardState depending on the code.
//
// The caller must also drop scope-tagged messages for a credential it has not presented, and
// for a presented credential it does not yet hold anything other than the intent and an
// error. That filtering is not optional and this store cannot do it, because presentation is
// connection state the store does not track. It matters most right after a removal: a
// delivery the server sent before it processed the withdrawal would otherwise reach a scope
// this store no longer holds, and be rejected as a resume with no membership.
//
// An error means the message violates the contract in a way this store will not paper over:
//
//   - a required field is absent, or an object identity is not valid UTF-8;
//   - an intent has an unrecognized code or omits its unfiltered declaration;
//   - object content is absent or is not UTF-8 JSON, or a selector is not UTF-8;
//   - the server sent a ref to a scope it declared unfiltered;
//   - the server resumed a scope it cannot resume.
//
// The store keeps its objects and membership unchanged. A rejected resume also abandons the
// delivery before it, and one that changes the declaration discards the scope's selector, so
// that the reconnect hydrates the scope in full instead of presenting the same selector
// again. The caller's remedy is to report the frame and reconnect rather than to continue
// applying.
func (s *Store) Apply(msg any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch typed := msg.(type) {
	case wire.ServerIntent:
		return s.applyIntent(typed)
	case wire.PutObject:
		return s.applyPut(typed)
	case wire.DeleteObject:
		return s.applyDelete(typed)
	case wire.Ref:
		return s.addRef(typed.Scope, ObjectID{Kind: typed.Kind, Key: typed.Key})
	case wire.RefDelete:
		return s.removeRef(typed.Scope, ObjectID{Kind: typed.Kind, Key: typed.Key})
	case wire.RefBatch:
		return s.applyRefBatch(typed)
	case wire.PayloadTransferred:
		return s.applyCursor(typed)
	default:
		return nil
	}
}

// applyRefBatch applies a batch of membership changes.
//
// Every entry is checked before any is applied, so a batch either applies whole or not at
// all. Holding the write lock would hide a partial batch from readers but would not undo it,
// and a caller told that a rejected message changed nothing has to be able to rely on that.
func (s *Store) applyRefBatch(batch wire.RefBatch) error {
	for _, ref := range batch.Refs {
		if err := s.checkRef(batch.Scope, ObjectID{Kind: ref.Kind, Key: ref.Key}); err != nil {
			return err
		}
	}
	for _, ref := range batch.Deletes {
		if err := s.checkRef(batch.Scope, ObjectID{Kind: ref.Kind, Key: ref.Key}); err != nil {
			return err
		}
	}
	for _, ref := range batch.Refs {
		if err := s.addRef(batch.Scope, ObjectID{Kind: ref.Kind, Key: ref.Key}); err != nil {
			return err
		}
	}
	for _, ref := range batch.Deletes {
		if err := s.removeRef(batch.Scope, ObjectID{Kind: ref.Kind, Key: ref.Key}); err != nil {
			return err
		}
	}
	return nil
}

// checkRef reports whether a membership change names something this store can act on,
// without applying it.
func (s *Store) checkRef(name string, id ObjectID) error {
	if name == "" || !id.addressable() {
		return fmt.Errorf("megastream: ref names no scope or no valid object (scope %q, object %q)",
			masked(name), id)
	}
	if sc := s.scopes[name]; sc != nil && sc.declaredUnfiltered {
		return fmt.Errorf("megastream: ref for scope %q, which was declared unfiltered",
			masked(name))
	}
	return nil
}

// applyIntent opens a scope's delivery.
//
// A full transfer for a scope that already holds membership is a resync: the scope rebuilds
// from the delivery that follows and keeps nothing the delivery does not re-assert. A
// changes transfer patches the membership in place.
func (s *Store) applyIntent(intent wire.ServerIntent) error {
	if intent.Scope == "" {
		return errors.New("megastream: server-intent names no scope")
	}

	// The unfiltered declaration is explicit on every intent, and membership is never
	// inferred from an absence of refs: an empty view has no refs either, so treating a
	// missing declaration as "filtered" would silently reduce an environment-wide scope to
	// serving nothing.
	unfiltered, declared := intent.IsUnfiltered()
	if !declared {
		return fmt.Errorf(
			"megastream: server-intent for scope %q omits its unfiltered declaration",
			masked(intent.Scope))
	}

	// The schema closes the intent codes to three values, so anything else is a contract
	// violation rather than a code to guess at. Guessing either way is unsafe: read as a
	// delta it would retain membership a rebuild meant to discard, and read as a rebuild it
	// would blank a hydrated scope and then record a presentable selector for it -- which is
	// the same silent, sticky under-serving that the rebuild-scoping bug produced.
	resuming := intent.IntentCode == wire.IntentTransferChanges || intent.IntentCode == wire.IntentNone
	if !resuming && intent.IntentCode != wire.IntentTransferFull {
		return fmt.Errorf("megastream: server-intent for scope %q has unrecognized code %q",
			masked(intent.Scope), intent.IntentCode)
	}

	sc := s.scopes[intent.Scope]
	if resuming {
		if err := s.checkResume(intent, sc, unfiltered); err != nil {
			if sc != nil {
				// The rejected resume still ends the delivery before it. Leaving that
				// delivery's buffer in place would let a later cursor install it.
				sc.rebuilding = nil
				sc.declaredUnfiltered = sc.unfiltered
			}
			return err
		}
	}
	if sc == nil {
		// Nothing is served before the first cursor, so a new scope takes its declaration
		// at once.
		sc = &scope{
			members: map[ObjectID]struct{}{},
			pending: map[ObjectID]struct{}{},
		}
		s.scopes[intent.Scope] = sc
		s.setUnfiltered(intent.Scope, sc, unfiltered)
	}

	// An intent opens a new delivery, which abandons whatever the last one was building.
	// Without this the buffer outlives its delivery: an interrupted full transfer would
	// leave it live, the next delivery's refs would route into it, and that delivery's
	// cursor would install it as the scope's whole membership -- silently dropping every
	// member the new delivery did not happen to re-assert, or all of them when the new
	// intent is the degenerate "nothing changed" resume.
	sc.rebuilding = nil
	sc.declaredUnfiltered = unfiltered
	if !sc.hydrated && sc.unfiltered != unfiltered {
		// A scope serves nothing before its first cursor, so there is no old membership to
		// keep serving, and staging would only route this delivery's fan-out by the old
		// declaration. Such a scope holds no live membership, because its refs went to the
		// rebuild buffer.
		s.releaseMembership(intent.Scope, sc)
		s.setUnfiltered(intent.Scope, sc, unfiltered)
	}

	// A full transfer rebuilds the membership from this delivery alone; a resume patches
	// what the scope already holds. A change of declaration waits for the cursor too, so
	// the scope keeps serving its old membership under its old selector until then.
	if !resuming && !unfiltered {
		sc.rebuilding = map[ObjectID]struct{}{}
	}
	return nil
}

// checkResume reports whether a resume can apply to the scope. A rejection discards the
// scope's selector, so that the reconnect does not present it again.
func (s *Store) checkResume(intent wire.ServerIntent, sc *scope, unfiltered bool) error {
	// A resume answers a state this relay presented, and its delivery carries only what
	// changed since that state. A relay presents no state for a scope it holds no
	// membership for, for one that never completed its first delivery, or for one whose
	// selector was discarded. With nothing to apply the delta to, the rest of the scope's
	// membership will never arrive, and the scope would look complete while serving a
	// fraction of the credential's objects. Meeting this pairing means something upstream
	// is inconsistent, and a full hydration is the only sound answer.
	//
	// Pending references are deliberately not part of this test. State withholds while a
	// reference is pending, but whether this relay presented a state was decided at the
	// handshake. Base-scope traffic may arrive before this scope's intent, and an eviction
	// there makes a presented scope pending again. Its delta then resolves the reference.
	if sc == nil || !sc.hydrated || sc.state == "" {
		return fmt.Errorf(
			"megastream: scope %q resumed with %q but this relay presented no state for it",
			masked(intent.Scope), intent.IntentCode)
	}

	// A delta is relative to the membership the presented state describes. An unfiltered
	// scope holds no membership set, so a delta that makes it filtered applies to nothing,
	// and a delta that makes a filtered scope unfiltered has no meaning. The server sends a
	// full transfer when it cannot compute the exact difference, so a resume that changes
	// the declaration is a contract violation.
	if unfiltered != sc.unfiltered {
		sc.discardState()
		return fmt.Errorf(
			"megastream: scope %q resumed with %q but changed its unfiltered declaration",
			masked(intent.Scope), intent.IntentCode)
	}
	return nil
}

// applyPut stores an object's content and resolves any pending reference to it.
func (s *Store) applyPut(put wire.PutObject) error {
	if !(ObjectID{Kind: put.Kind, Key: put.Key}).addressable() {
		return errors.New("megastream: put-object names no valid object")
	}
	if len(put.Object) == 0 {
		// Content is required. Storing a row without it would leave every scope referencing
		// the object holding a member that is neither servable nor pending, which in turn
		// makes that scope's selector presentable for a state whose content this relay does
		// not have -- so the server would skip exactly that object on the next resume.
		return fmt.Errorf("megastream: put-object for %s/%s carries no content",
			put.Kind, put.Key)
	}
	if !json.Valid(put.Object) || !utf8.Valid(put.Object) {
		// Every byte-through surface splices the content into a larger JSON document, so
		// malformed content would corrupt the document for every scope that sees it. JSON
		// text must be UTF-8, and the JSON check alone admits invalid bytes inside a string.
		// Whitespace between tokens is legal JSON and is kept: a surface that writes lines,
		// such as a stream, must neutralize a carriage return itself. The shape of the value
		// is not checked, because kinds are an open set.
		return fmt.Errorf("megastream: put-object for %s/%s carries content that is not JSON",
			put.Kind, put.Key)
	}
	id := ObjectID{Kind: put.Kind, Key: put.Key}
	_, held := s.objects[id]
	s.objects[id] = &Record{
		version: put.Version,
		raw:     put.Object,
		kind:    put.Kind,
	}
	// Content arriving resolves the reference for every scope that was waiting on it.
	for name := range s.referencing[id] {
		if sc := s.scopes[name]; sc != nil {
			delete(sc.pending, id)
		}
	}
	if !held {
		for _, sc := range s.unfiltered {
			if _, lost := sc.evicted[id]; lost {
				// The selector claims this object again.
				delete(sc.evicted, id)
			} else if sc.state != "" {
				// The store did not hold it at the last cursor, so the selector does not
				// claim it.
				sc.created = addTo(sc.created, id)
			}
		}
	}
	return nil
}

// applyDelete evicts an object and leaves every scope's membership untouched.
//
// Eviction carries no membership meaning of its own. An object leaves the base scope only by
// leaving every registered scope's effective set, so the matching ref-deletes arrive as
// ordinary membership traffic -- or the object re-enters first and fresh content resolves the
// reference instead. Until one of those happens the reference is pending again, which is the
// same normal transient as a ref that outran its content.
func (s *Store) applyDelete(del wire.DeleteObject) error {
	if !(ObjectID{Kind: del.Kind, Key: del.Key}).addressable() {
		return errors.New("megastream: delete-object names no valid object")
	}
	id := ObjectID{Kind: del.Kind, Key: del.Key}
	if _, held := s.objects[id]; !held {
		// A delete for an object the store does not hold is a no-op.
		return nil
	}

	// The row goes entirely, rather than leaving a tombstone.
	//
	// A tombstone would exist to lose a version comparison against a later stale put, and
	// this store deliberately makes no such comparison: the single ordered stream is the
	// ordering authority, and a membership-driven re-entry may legitimately repeat a
	// version, so gating on one would drop a real change. With no comparison to serve, a
	// tombstone is a row nothing reads that nothing ever reclaims -- and the store grows in
	// the number of keys ever seen rather than the number held. The object version a
	// downstream FDv1 consumer compares against lives inside the content, not here, and
	// belongs to the translation layer that serves it.
	delete(s.objects, id)

	// Membership is untouched: content and membership are separate planes. A reference to
	// the evicted object is pending again, which is the same ordinary transient as a ref
	// that outran its content.
	for name := range s.referencing[id] {
		if sc := s.scopes[name]; sc != nil {
			sc.pending[id] = struct{}{}
		}
	}
	// An unfiltered scope may see every object, so it loses this one too, without holding a
	// reference that could go pending.
	for _, sc := range s.unfiltered {
		if _, created := sc.created[id]; created {
			delete(sc.created, id)
		} else if sc.state != "" {
			// A scope with no state has no selector to protect.
			sc.evicted = addTo(sc.evicted, id)
		}
	}
	return nil
}

// addRef adds an object to a scope's membership.
func (s *Store) addRef(name string, id ObjectID) error {
	if name == "" || !id.addressable() {
		// An absent required field is not the open-set tolerance the protocol asks for.
		// Admitting one would enter a membership entry no put-object can ever address,
		// which would leave the scope's selector ungateable and its resume broken.
		return fmt.Errorf("megastream: ref names no scope or no valid object (scope %q, object %q)",
			masked(name), id)
	}
	sc := s.scopes[name]
	if sc == nil {
		// A ref for a scope with no intent belongs to a registration this store never saw
		// opened, which the protocol says to ignore. Dropping it is safer than inventing
		// the scope, because the unfiltered declaration only arrives on the intent.
		s.logger.Debug("ignoring a ref for an unopened scope", "scope", masked(name),
			"object", id)
		return nil
	}
	if sc.declaredUnfiltered {
		// An unfiltered scope receives no refs at all, so one arriving means this relay and
		// the server disagree about whether the credential is filtered. That disagreement
		// is not safe to paper over: if the refs are the truth then the declaration was
		// wrong and the scope is being served the whole base store. Report it and let the
		// caller reconnect rather than logging and continuing.
		return fmt.Errorf("megastream: ref for scope %q, which was declared unfiltered",
			masked(name))
	}

	if sc.rebuilding != nil {
		// Only the live membership drives fan-out and pending, so a rebuild stays invisible
		// until it is swapped in.
		sc.rebuilding[id] = struct{}{}
		return nil
	}
	if _, member := sc.members[id]; !member && sc.state != "" {
		if _, retracted := sc.retracted[id]; retracted {
			// The selector claims the object, so re-adding it undoes the retraction.
			delete(sc.retracted, id)
		} else {
			sc.added = addTo(sc.added, id)
		}
	}
	sc.members[id] = struct{}{}
	s.reference(id, name)
	if !s.objects[id].Present() {
		sc.pending[id] = struct{}{}
	}
	return nil
}

// removeRef drops an object from a scope's membership.
func (s *Store) removeRef(name string, id ObjectID) error {
	if name == "" || !id.addressable() {
		return fmt.Errorf(
			"megastream: ref-delete names no scope or no valid object (scope %q, object %q)",
			masked(name), id)
	}
	sc := s.scopes[name]
	if sc == nil {
		s.logger.Debug("ignoring a ref-delete for an unopened scope", "scope", masked(name),
			"object", id)
		return nil
	}
	if sc.declaredUnfiltered {
		// Same relay-and-server disagreement the ref path refuses to paper over.
		return fmt.Errorf("megastream: ref-delete for scope %q, which was declared unfiltered",
			masked(name))
	}

	// During a rebuild the live membership is what the scope is still serving, and the
	// rebuild is what will replace it at the cursor. A retraction therefore applies to the
	// set being built, not to the one being served -- otherwise a delivery that is later
	// abandoned would have permanently narrowed what the scope serves.
	if sc.rebuilding != nil {
		delete(sc.rebuilding, id)
		return nil
	}
	if _, added := sc.added[id]; added {
		// The selector does not claim an object added after it.
		delete(sc.added, id)
	} else if _, member := sc.members[id]; member && sc.state != "" {
		sc.retracted = addTo(sc.retracted, id)
	}
	delete(sc.members, id)
	delete(sc.pending, id)
	s.unreference(id, name)
	return nil
}

// applyCursor records a scope's selector and completes any full transfer in flight.
func (s *Store) applyCursor(cursor wire.PayloadTransferred) error {
	if cursor.Scope == "" {
		return errors.New("megastream: payload-transferred names no scope")
	}
	if cursor.State == "" {
		// The selector is what the next handshake presents to resume. An empty one would
		// overwrite a good selector with nothing while still marking the scope hydrated,
		// costing that scope its resume on every later reconnect.
		return fmt.Errorf("megastream: payload-transferred for scope %q carries no state",
			masked(cursor.Scope))
	}
	if !utf8.ValidString(cursor.State) {
		// The selector goes back to the server in the handshake and downstream as an Etag,
		// both of which are text. An encoder would replace the invalid bytes, so the
		// presented selector would not be the one the server issued.
		return fmt.Errorf("megastream: payload-transferred for scope %q carries a state that is not UTF-8",
			masked(cursor.Scope))
	}
	sc := s.scopes[cursor.Scope]
	if sc == nil {
		s.logger.Debug("ignoring a cursor for an unopened scope", "scope", masked(cursor.Scope))
		return nil
	}
	if sc.declaredUnfiltered && !sc.unfiltered {
		// An unfiltered scope carries no membership set, so release the one it had.
		s.releaseMembership(cursor.Scope, sc)
	}
	s.setUnfiltered(cursor.Scope, sc, sc.declaredUnfiltered)
	if sc.rebuilding != nil {
		s.swapMembership(cursor.Scope, sc)
	}
	sc.state = cursor.State
	sc.clearClaims()
	sc.hydrated = true
	return nil
}

// swapMembership replaces a scope's membership with the one a full-transfer delivery built,
// at the delivery's cursor. Nothing the delivery did not re-assert survives.
func (s *Store) swapMembership(name string, sc *scope) {
	s.releaseMembership(name, sc)
	sc.members = sc.rebuilding
	sc.rebuilding = nil
	for id := range sc.members {
		s.reference(id, name)
		if !s.objects[id].Present() {
			sc.pending[id] = struct{}{}
		}
	}
}

// releaseMembership empties a scope's live membership and removes it from the reverse index.
func (s *Store) releaseMembership(name string, sc *scope) {
	for id := range sc.members {
		s.unreference(id, name)
	}
	sc.members = map[ObjectID]struct{}{}
	sc.pending = map[ObjectID]struct{}{}
	sc.clearClaims()
}

// setUnfiltered sets the declaration a scope is served under and keeps the index in step.
func (s *Store) setUnfiltered(name string, sc *scope, unfiltered bool) {
	sc.unfiltered = unfiltered
	if unfiltered {
		s.unfiltered[name] = sc
	} else {
		delete(s.unfiltered, name)
	}
}

// changedSinceCursor reports whether the scope's membership differs from what its selector
// names. The sets net out a change that was undone before the cursor, so a ref followed by
// its ref-delete leaves the scope unchanged.
func (sc *scope) changedSinceCursor() bool {
	return len(sc.added) > 0 || len(sc.retracted) > 0 || len(sc.created) > 0
}

// addTo adds an id to a set, allocating the set on first use.
func addTo(set map[ObjectID]struct{}, id ObjectID) map[ObjectID]struct{} {
	if set == nil {
		set = map[ObjectID]struct{}{}
	}
	set[id] = struct{}{}
	return set
}

// RemoveScope discards a scope's entire membership and any state recorded for it.
//
// Removal is wholesale: the server sends no per-object ref-deletes for it. Discarding the
// recorded state alongside the membership is what stops a later registration of the same
// credential from resuming a delta onto an empty membership.
func (s *Store) RemoveScope(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sc := s.scopes[name]
	if sc == nil {
		return
	}
	for id := range sc.members {
		s.unreference(id, name)
	}
	sc.clearClaims()
	delete(s.scopes, name)
	delete(s.unfiltered, name)
}

// DiscardState drops a scope's recorded selector while keeping its membership.
//
// This is the pairing a refused registration needs. A refusal means the server could not
// decide, not that the credential is bad, so the membership is retained and that key's
// downstream keeps being served. The selector goes for the opposite reason: while the
// credential is unregistered the base store may lose objects only it referenced, and a delta
// resume from the stale cursor would never re-send them. With no state to present, the next
// registration is a full hydration, which is what is wanted.
func (s *Store) DiscardState(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sc := s.scopes[name]; sc != nil {
		sc.discardState()
	}
}

func (sc *scope) discardState() {
	sc.state = ""
	sc.clearClaims()
}

// clearClaims drops the sets that describe what a scope's selector claims, for when the
// selector is replaced or discarded.
func (sc *scope) clearClaims() {
	sc.retracted = nil
	sc.added = nil
	sc.evicted = nil
	sc.created = nil
}

// addressable reports whether an identity names an object that an object message can
// address and a downstream document can carry. Downstream surfaces write the kind and key
// into JSON, so both must be valid UTF-8.
func (id ObjectID) addressable() bool {
	return id.Kind != "" && id.Key != "" && utf8.ValidString(id.Kind) && utf8.ValidString(id.Key)
}

// masked shortens a scope name for errors and logs. A scope name is a credential, and an
// SDK key must not reach the logs in full.
func masked(name string) string {
	if len(name) <= 4 {
		return name
	}
	return "..." + name[len(name)-4:]
}

// reference and unreference maintain the reverse index.
func (s *Store) reference(id ObjectID, name string) {
	scopes := s.referencing[id]
	if scopes == nil {
		scopes = map[string]struct{}{}
		s.referencing[id] = scopes
	}
	scopes[name] = struct{}{}
}

func (s *Store) unreference(id ObjectID, name string) {
	scopes := s.referencing[id]
	if scopes == nil {
		return
	}
	delete(scopes, name)
	if len(scopes) == 0 {
		delete(s.referencing, id)
	}
}
