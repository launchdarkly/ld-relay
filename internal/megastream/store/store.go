package store

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

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
}

// scope is one registered credential's view of the base store.
type scope struct {
	// unfiltered scopes may see every object in the base store. They receive no refs, so
	// they carry no membership set: materializing one would cost per-object bookkeeping for
	// the commonest configuration, a plain environment key with no views.
	unfiltered bool

	// members is the scope's current membership, empty for an unfiltered scope.
	members map[ObjectID]struct{}

	// rebuilding is non-nil while a full-transfer delivery is in flight. Refs land here and
	// replace members at the delivery's cursor, so a resync never exposes a half-built
	// membership.
	rebuilding map[ObjectID]struct{}

	// pending is the subset of members whose content the store does not hold. A ref asserts
	// membership; the base store is the source of truth for existence, so a reference to an
	// object not held is a normal transient rather than an error.
	pending map[ObjectID]struct{}

	// state is the selector from the most recent applied cursor, and version the payload
	// version it advanced to.
	state   string
	version wire.PayloadVersion

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
// a required field is absent, or the server sent a ref to a scope it declared unfiltered.
// Both leave the store unchanged, and the caller's remedy is to report the frame and
// reconnect rather than to continue applying.
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
	if name == "" || id.Kind == "" || id.Key == "" {
		return fmt.Errorf("megastream: ref names no scope or no object (scope %q, object %q)",
			name, id)
	}
	if sc := s.scopes[name]; sc != nil && sc.unfiltered {
		return fmt.Errorf("megastream: ref for scope %q, which was declared unfiltered", name)
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
			intent.Scope)
	}

	// The schema closes the intent codes to three values, so anything else is a contract
	// violation rather than a code to guess at. Guessing either way is unsafe: read as a
	// delta it would retain membership a rebuild meant to discard, and read as a rebuild it
	// would blank a hydrated scope and then record a presentable selector for it -- which is
	// the same silent, sticky under-serving that the rebuild-scoping bug produced.
	resuming := intent.IntentCode == wire.IntentTransferChanges || intent.IntentCode == wire.IntentNone
	if !resuming && intent.IntentCode != wire.IntentTransferFull {
		return fmt.Errorf("megastream: server-intent for scope %q has unrecognized code %q",
			intent.Scope, intent.IntentCode)
	}

	sc := s.scopes[intent.Scope]
	if sc == nil {
		if resuming {
			// A resume answers a state this relay presented, and its delivery carries only
			// what changed since that state. With no membership to apply the delta to, the
			// rest of the scope's membership will never arrive, and the resulting scope
			// would look complete while serving a fraction of the credential's objects. The
			// protocol discards a recorded state alongside its membership precisely so this
			// pairing cannot occur, so encountering it means something upstream is
			// inconsistent and a full hydration is the only sound answer.
			return fmt.Errorf(
				"megastream: scope %q resumed with %q but this store holds no membership for it",
				intent.Scope, intent.IntentCode)
		}
		sc = &scope{
			members: map[ObjectID]struct{}{},
			pending: map[ObjectID]struct{}{},
		}
		s.scopes[intent.Scope] = sc
	}

	// An intent opens a new delivery, which abandons whatever the last one was building.
	// Without this the buffer outlives its delivery: an interrupted full transfer would
	// leave it live, the next delivery's refs would route into it, and that delivery's
	// cursor would install it as the scope's whole membership -- silently dropping every
	// member the new delivery did not happen to re-assert, or all of them when the new
	// intent is the degenerate "nothing changed" resume.
	sc.rebuilding = nil

	if sc.unfiltered != unfiltered {
		sc.unfiltered = unfiltered
		if unfiltered {
			// An unfiltered scope carries no membership set, so release the one it had.
			for id := range sc.members {
				s.unreference(id, intent.Scope)
			}
			sc.members = map[ObjectID]struct{}{}
			sc.pending = map[ObjectID]struct{}{}
		}
	}

	// A full transfer rebuilds the membership from this delivery alone; a resume patches
	// what the scope already holds.
	if !resuming && !sc.unfiltered {
		sc.rebuilding = map[ObjectID]struct{}{}
	}
	return nil
}

// applyPut stores an object's content and resolves any pending reference to it.
func (s *Store) applyPut(put wire.PutObject) error {
	if put.Kind == "" || put.Key == "" {
		return errors.New("megastream: put-object names no object")
	}
	if put.Object == nil {
		// Content is required. Storing a row without it would leave every scope referencing
		// the object holding a member that is neither servable nor pending, which in turn
		// makes that scope's selector presentable for a state whose content this relay does
		// not have -- so the server would skip exactly that object on the next resume.
		return fmt.Errorf("megastream: put-object for %s/%s carries no content",
			put.Kind, put.Key)
	}
	id := ObjectID{Kind: put.Kind, Key: put.Key}
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
	if del.Kind == "" || del.Key == "" {
		return errors.New("megastream: delete-object names no object")
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
	return nil
}

// addRef adds an object to a scope's membership.
func (s *Store) addRef(name string, id ObjectID) error {
	if name == "" || id.Kind == "" || id.Key == "" {
		// An absent required field is not the open-set tolerance the protocol asks for.
		// Admitting one would enter a membership entry no put-object can ever address,
		// which would leave the scope's selector ungateable and its resume broken.
		return fmt.Errorf("megastream: ref names no scope or no object (scope %q, object %q)",
			name, id)
	}
	sc := s.scopes[name]
	if sc == nil {
		// A ref for a scope with no intent belongs to a registration this store never saw
		// opened, which the protocol says to ignore. Dropping it is safer than inventing
		// the scope, because the unfiltered declaration only arrives on the intent.
		s.logger.Debug("ignoring a ref for an unopened scope", "scope", name, "object", id)
		return nil
	}
	if sc.unfiltered {
		// An unfiltered scope receives no refs at all, so one arriving means this relay and
		// the server disagree about whether the credential is filtered. That disagreement
		// is not safe to paper over: if the refs are the truth then the declaration was
		// wrong and the scope is being served the whole base store. Report it and let the
		// caller reconnect rather than logging and continuing.
		return fmt.Errorf("megastream: ref for scope %q, which was declared unfiltered", name)
	}

	target := sc.members
	if sc.rebuilding != nil {
		target = sc.rebuilding
	}
	target[id] = struct{}{}

	// Only the live membership drives fan-out and pending, so a rebuild stays invisible
	// until it is swapped in.
	if sc.rebuilding == nil {
		s.reference(id, name)
		if !s.objects[id].Present() {
			sc.pending[id] = struct{}{}
		}
	}
	return nil
}

// removeRef drops an object from a scope's membership.
func (s *Store) removeRef(name string, id ObjectID) error {
	if name == "" || id.Kind == "" || id.Key == "" {
		return fmt.Errorf("megastream: ref-delete names no scope or no object (scope %q, object %q)",
			name, id)
	}
	sc := s.scopes[name]
	if sc == nil {
		s.logger.Debug("ignoring a ref-delete for an unopened scope", "scope", name, "object", id)
		return nil
	}
	if sc.unfiltered {
		// Same relay-and-server disagreement the ref path refuses to paper over.
		return fmt.Errorf("megastream: ref-delete for scope %q, which was declared unfiltered",
			name)
	}

	// During a rebuild the live membership is what the scope is still serving, and the
	// rebuild is what will replace it at the cursor. A retraction therefore applies to the
	// set being built, not to the one being served -- otherwise a delivery that is later
	// abandoned would have permanently narrowed what the scope serves.
	if sc.rebuilding != nil {
		delete(sc.rebuilding, id)
		return nil
	}
	delete(sc.members, id)
	delete(sc.pending, id)
	s.unreference(id, name)
	return nil
}

// applyCursor records a scope's selector and completes any rebuild in flight.
func (s *Store) applyCursor(cursor wire.PayloadTransferred) error {
	if cursor.Scope == "" {
		return errors.New("megastream: payload-transferred names no scope")
	}
	if cursor.State == "" {
		// The selector is what the next handshake presents to resume. An empty one would
		// overwrite a good selector with nothing while still marking the scope hydrated,
		// costing that scope its resume on every later reconnect.
		return fmt.Errorf("megastream: payload-transferred for scope %q carries no state",
			cursor.Scope)
	}
	sc := s.scopes[cursor.Scope]
	if sc == nil {
		s.logger.Debug("ignoring a cursor for an unopened scope", "scope", cursor.Scope)
		return nil
	}
	if sc.rebuilding != nil {
		s.swapMembership(cursor.Scope, sc)
	}
	sc.state = cursor.State
	sc.version = cursor.Version
	sc.hydrated = true
	return nil
}

// swapMembership replaces a scope's membership with the one a full-transfer delivery built,
// at the delivery's cursor. Nothing the delivery did not re-assert survives.
func (s *Store) swapMembership(name string, sc *scope) {
	for id := range sc.members {
		s.unreference(id, name)
	}
	sc.members = sc.rebuilding
	sc.rebuilding = nil
	sc.pending = map[ObjectID]struct{}{}
	for id := range sc.members {
		s.reference(id, name)
		if !s.objects[id].Present() {
			sc.pending[id] = struct{}{}
		}
	}
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
	delete(s.scopes, name)
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

	sc := s.scopes[name]
	if sc == nil {
		return
	}
	sc.state = ""
	sc.version = 0
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
