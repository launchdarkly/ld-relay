package registry

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/session"
	"github.com/launchdarkly/ld-relay/v9/internal/megastream/store"
	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
	"github.com/launchdarkly/ld-relay/v9/internal/retry"
)

const (
	// ReconcileInterval is how often the owner of a registry calls Reconcile when no message
	// prompts it. The timer is what makes a failed send, a lapsed expiry or a due retry take
	// effect without a message arriving.
	ReconcileInterval = 5 * time.Second

	// retryInitialDelay and retryNormalCeiling bound the retries after a failure that clears
	// with time: a verification outage or a rate limit.
	retryInitialDelay  = 5 * time.Second
	retryNormalCeiling = 5 * time.Minute

	// retryExtendedInitialDelay and retryExtendedCeiling bound the retries after a failure
	// that needs an operator or another scope to leave. They are slow and capped because a
	// rejection cannot be told apart from a key that is not valid yet.
	retryExtendedInitialDelay = 1 * time.Minute
	retryExtendedCeiling      = 30 * time.Minute
)

// Sender is the part of a session the registry drives. The session satisfies it.
type Sender interface {
	Register(ctx context.Context, key string) error
	Deregister(ctx context.Context, scope string) error
}

// Option changes how a Registry measures time or backs off. Tests use these to make the
// output deterministic.
type Option func(*Registry)

// WithClock replaces the clock that expiries and retries read.
func WithClock(now func() time.Time) Option {
	return func(r *Registry) { r.now = now }
}

// WithRetryOptions passes options to each scope's retry strategy.
func WithRetryOptions(options ...retry.Option) Option {
	return func(r *Registry) { r.retryOptions = options }
}

// Registry reconciles the credentials configuration wants against what the server has said,
// and drives the store's scope lifecycle.
//
// Apply and Reconcile must be called from one goroutine, the one that consumes the session's
// messages. The other methods are safe to call from any goroutine.
type Registry struct {
	logger       *slog.Logger
	store        *store.Store
	now          func() time.Time
	retryOptions []retry.Option

	mu     sync.Mutex
	scopes map[string]*scope
	// root is the credential naming the session. It authorizes the connection and bounds
	// what the base scope may carry, and it contributes no content unless it is also wanted
	// as a scope in its own right.
	root string
	// maxScopes is the bound the server advertised on an earlier session, or zero for none.
	// It is remembered across connections, because the protocol expects a relay to cap what
	// it presents rather than discover the limit one rejection at a time.
	maxScopes int
	// connected records whether the session has an established connection.
	connected bool
	// hello holds the credentials the latest hello named, until the connection that
	// presented it is applied. The server may hold any of them by then, together with the
	// selector that the hello offered for it.
	hello map[string]struct{}
}

// New builds a registry over a store.
func New(logger *slog.Logger, objects *store.Store, options ...Option) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Registry{
		logger: logger.With("component", "MegaStreamRegistry"),
		store:  objects,
		now:    time.Now,
		scopes: map[string]*scope{},
	}
	for _, option := range options {
		option(r)
	}
	return r
}

// SetDesired replaces the wanted credential set wholesale, which is how configuration and
// auto-configuration both deliver it. A rotation is the new key appearing and the old one
// gaining an expiry; nothing has to diff anything.
//
// The root is separate from the scopes. A relay that also serves SDKs with its environment
// key includes that key among the scopes as well, and a relay that serves only views does
// not, which keeps the base scope down to those views' members. A new root takes effect at
// the next handshake, because the root cannot change during a session.
//
// SetDesired only records state. Reconcile sends whatever the change calls for.
func (r *Registry) SetDesired(root string, wanted []Desired) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.root = root

	seen := make(map[string]struct{}, len(wanted))
	for _, want := range wanted {
		seen[want.Key] = struct{}{}
		if sc := r.scopes[want.Key]; sc != nil {
			sc.desired = want
			sc.removed = false
			continue
		}
		r.scopes[want.Key] = &scope{desired: want}
	}
	for key, sc := range r.scopes {
		if _, still := seen[key]; !still {
			sc.removed = true
		}
	}
}

// Hello assembles a handshake. The session calls it before every connection attempt, so a
// reconnect presents whatever is currently wanted, with the latest selectors the store may
// offer. It has the session.HelloProvider signature.
//
// Every wanted credential the scope bound allows is presented, including one that was
// rejected or refused. A reconnect is the natural point to try it again.
//
// The session calls Hello on its own goroutine, and the presentation takes effect when the
// Connected that names it is applied, in order with the messages. Until then, Hello only
// keeps every credential it named from being forgotten.
func (r *Registry) Hello() (string, []wire.HelloScope) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	var wanted []string
	for key, sc := range r.scopes {
		if sc.wants(now) {
			wanted = append(wanted, key)
		}
	}
	keys := r.boundLocked(wanted, 0)
	if left := len(wanted) - len(keys); left > 0 {
		// Logged once per handshake rather than on every reconcile, which runs often.
		r.logger.Warn("more credentials are configured than this session may register",
			"maxScopes", r.maxScopes, "unregistered", left)
	}
	scopes := make([]wire.HelloScope, 0, len(keys))
	r.hello = make(map[string]struct{}, len(keys))
	for _, key := range keys {
		r.hello[key] = struct{}{}
		entry := wire.HelloScope{Key: key}
		// The store withholds a selector it cannot stand behind, so the server answers a
		// withheld one with a full transfer.
		if selector, ok := r.store.State(key); ok {
			entry.State = selector
		}
		scopes = append(scopes, entry)
	}
	return r.root, scopes
}

// boundLocked orders candidates and keeps as many as the scope bound has room for, given that
// occupied slots are already taken. The caller holds the lock.
//
// When the configuration exceeds the bound, a credential that has been served keeps its
// claim over one that has not, so a growing configuration does not change which keys are
// served. Ties break by key, so the choice is the same on every connection.
func (r *Registry) boundLocked(candidates []string, occupied int) []string {
	sort.Slice(candidates, func(i, j int) bool {
		a, b := r.scopes[candidates[i]], r.scopes[candidates[j]]
		if a.servable != b.servable {
			return a.servable
		}
		return candidates[i] < candidates[j]
	})
	if r.maxScopes <= 0 {
		return candidates
	}
	room := max(r.maxScopes-occupied, 0)
	if len(candidates) > room {
		candidates = candidates[:room]
	}
	return candidates
}

// Apply consumes one value from the session's output stream.
//
// Apply records state only. Reconcile does all the sending, so a send that fails is retried
// on the next call without any special recovery.
func (r *Registry) Apply(msg any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch typed := msg.(type) {
	case session.Connected:
		r.onConnectedLocked(typed)
	case session.Disconnected:
		r.onDisconnectedLocked()
	case wire.ServerIntent:
		r.onIntentLocked(typed)
	case wire.PayloadTransferred:
		r.onCursorLocked(typed)
	case wire.ErrorMessage:
		r.onErrorLocked(typed)
	case wire.Goodbye:
		r.onGoodbyeLocked(typed)
	}
}

func (r *Registry) onConnectedLocked(connected session.Connected) {
	r.connected = true
	if connected.Welcome.MaxScopes > 0 {
		r.maxScopes = connected.Welcome.MaxScopes
	}
	r.hello = nil
	for _, key := range connected.Presented {
		if sc := r.scopes[key]; sc != nil {
			sc.state = Presented
		}
	}
}

func (r *Registry) onDisconnectedLocked() {
	r.connected = false
	// Presentation is per connection, so nothing is on the wire any more. Servability is
	// untouched, because a disconnect is bounded staleness rather than an outage.
	for _, sc := range r.scopes {
		if sc.state.presented() {
			sc.state = Unregistered
		}
	}
}

// onIntentLocked records that the server opened a scope. A scope-tagged message for a
// credential that is not presented is ignored. It can only be one emitted before a withdrawal
// that the server has not processed yet.
func (r *Registry) onIntentLocked(intent wire.ServerIntent) {
	sc := r.scopes[intent.Scope]
	if sc == nil {
		return
	}
	switch sc.state {
	case Presented, Hydrating, Synced:
		sc.state = Hydrating
	}
}

func (r *Registry) onCursorLocked(cursor wire.PayloadTransferred) {
	sc := r.scopes[cursor.Scope]
	if sc == nil || sc.state != Hydrating && sc.state != Synced {
		return
	}
	sc.state = Synced
	sc.retry = nil
	sc.lastError = ""
	if !sc.servable {
		sc.servable = true
		r.logger.Info("MegaStream scope is now servable", "scope", masked(cursor.Scope))
	}
}

// onErrorLocked applies a scope-tagged error. The two classes do opposite things to the
// store, and conflating them loses data in one direction or blinds a credential's SDKs in the
// other.
func (r *Registry) onErrorLocked(report wire.ErrorMessage) {
	if report.Scope == "" {
		// An untagged error is advisory. Only a goodbye ends a session.
		r.logger.Warn("MegaStream reported a connection-level error",
			"code", report.Code, "message", report.Message)
		return
	}
	sc := r.scopes[report.Scope]
	if sc == nil || !sc.state.presented() {
		return
	}

	switch {
	case destroysScope(report.Code):
		r.destroyLocked(report.Scope, sc, report.Code)
		r.logger.Warn("MegaStream rejected a credential",
			"scope", masked(report.Scope), "code", report.Code)

	case refusesScope(report.Code):
		sc.state = Refused
		sc.lastError = report.Code
		r.scheduleRetryLocked(sc, report.Code)
		// The membership stays so the credential's SDKs keep being served. Only the
		// selector goes, so the next registration is a full transfer rather than a delta
		// onto a membership the base store may have lost objects from.
		r.store.DiscardState(report.Scope)
		r.logger.Warn("MegaStream refused a credential",
			"scope", masked(report.Scope), "code", report.Code)

	default:
		// An unrecognized code is informational. Guessing that it destroys the scope would
		// over-evict, and the protocol gates any code whose effect matters on a capability.
		r.logger.Info("MegaStream reported a scope-level error",
			"scope", masked(report.Scope), "code", report.Code, "message", report.Message)
	}
}

// onGoodbyeLocked handles the root's revocation. It ends the session in place of a
// scope-tagged error, so when the root is also registered as a scope, the goodbye is that
// scope's destruction too.
func (r *Registry) onGoodbyeLocked(goodbye wire.Goodbye) {
	if goodbye.Code != wire.GoodbyeRootRevoked {
		return
	}
	r.logger.Error("the MegaStream root credential was revoked", "scope", masked(r.root))
	if sc := r.scopes[r.root]; sc != nil && sc.state.presented() {
		r.destroyLocked(r.root, sc, wire.ErrorScopeRevoked)
	}
}

// destroyLocked discards a scope the server destroyed. Both the membership and the selector
// go, which stops a later presentation of the same credential from resuming a delta onto an
// empty membership.
func (r *Registry) destroyLocked(key string, sc *scope, code string) {
	sc.state = Rejected
	sc.servable = false
	sc.lastError = code
	r.scheduleRetryLocked(sc, code)
	r.store.RemoveScope(key)
}

func (r *Registry) scheduleRetryLocked(sc *scope, code string) {
	if sc.retry == nil {
		sc.retry = retry.NewStrategy(retry.Config{
			InitialDelay:         retryInitialDelay,
			NormalCeiling:        retryNormalCeiling,
			ExtendedInitialDelay: retryExtendedInitialDelay,
			ExtendedCeiling:      retryExtendedCeiling,
		}, r.retryOptions...)
	}
	sc.retry.OnFailure(failureClass(code))
	sc.retryAt = r.now().Add(sc.retry.NextWait())
}

// action is one message Reconcile sends.
type action struct {
	key      string
	register bool
	// previous is the state to restore when the send fails, so the next call tries again.
	previous State
}

// Reconcile sends whatever the difference between desired and wire state calls for. The
// owner calls it after each message and every ReconcileInterval.
//
//   - A wanted credential that is not on the wire is registered once its retry is due and
//     the scope bound has room for it.
//   - A credential that is no longer wanted is withdrawn once the server has answered its
//     presentation, and then forgotten. One that is not on the wire is forgotten at once.
func (r *Registry) Reconcile(ctx context.Context, sender Sender) {
	actions := r.plan()
	for _, act := range actions {
		var err error
		if act.register {
			err = sender.Register(ctx, act.key)
		} else {
			err = sender.Deregister(ctx, act.key)
		}
		r.settle(act, err)
	}
}

// plan decides the sends and claims their states before any send, so a message applied
// meanwhile sees the state the send produces.
func (r *Registry) plan() []action {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	var actions []action
	occupied := 0
	for key, sc := range r.scopes {
		if sc.wants(now) {
			if sc.state.occupiesSlot() {
				occupied++
			}
			continue
		}
		switch sc.state {
		case Presented:
			// The server has not answered yet. A withdrawal now would race the answer, so
			// the credential waits for its intent or its error. Until then it holds a slot.
			// A credential that is hydrating or synced holds one too, but its withdrawal is
			// sent before any registration in this call.
			occupied++
		case Hydrating, Synced, Refused:
			if r.connected {
				actions = append(actions, action{key: key, previous: sc.state})
			}
		default:
			if _, named := r.hello[key]; named {
				// The connection may present it with its selector. Forgetting it now would
				// leave the server holding a registration that is never withdrawn, and
				// would discard the store state that the server's resume applies to.
				// Connected marks it presented, and it is withdrawn after the answer.
				continue
			}
			r.forgetLocked(key)
		}
	}

	if !r.connected {
		return actions
	}
	var due []string
	for key, sc := range r.scopes {
		if sc.wants(now) && !sc.state.occupiesSlot() && !now.Before(sc.retryAt) {
			due = append(due, key)
		}
	}
	for _, key := range r.boundLocked(due, occupied) {
		sc := r.scopes[key]
		actions = append(actions, action{key: key, register: true, previous: sc.state})
		sc.state = Presented
	}
	return actions
}

// settle records the outcome of one send.
func (r *Registry) settle(act action, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sc := r.scopes[act.key]
	if sc == nil {
		return
	}
	if err != nil {
		if !errors.Is(err, session.ErrNotConnected) {
			r.logger.Debug("could not reconcile a MegaStream scope",
				"scope", masked(act.key), "error", err)
		}
		// Restore the state only when no message has changed it since the plan. A
		// disconnect, for one, has already taken the credential off the wire.
		if act.register && sc.state == Presented {
			sc.state = act.previous
		}
		return
	}
	if act.register {
		return
	}
	if sc.wants(r.now()) {
		// Configuration wanted the credential again while the withdrawal was sent. The
		// server has dropped the registration, so the store's scope goes, but the record
		// stays as a new credential that the next call registers with a full transfer.
		r.store.RemoveScope(act.key)
		r.scopes[act.key] = &scope{desired: sc.desired}
		return
	}
	r.forgetLocked(act.key)
}

// forgetLocked drops a withdrawn credential from both the registry and the store.
func (r *Registry) forgetLocked(key string) {
	delete(r.scopes, key)
	r.store.RemoveScope(key)
}

// Servable reports whether a credential may be served downstream, and its kind.
//
// Servability is a latch. It is set by the scope's first cursor and cleared only by
// destruction: a disconnect, a resync, a refusal and a re-hydration all leave a credential
// serving what it has, because bounded staleness beats an outage. A credential that
// configuration no longer wants, or whose expiry has passed, is not served.
func (r *Registry) Servable(key string) (Kind, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sc := r.scopes[key]
	if sc == nil || !sc.servable || !sc.wants(r.now()) {
		return 0, false
	}
	return sc.desired.Kind, true
}

// Status describes one credential, for the status endpoint.
type Status struct {
	Key       string
	Kind      Kind
	State     State
	Servable  bool
	LastError string
	Selector  string
}

// Statuses reports every credential the registry knows about, ordered by key.
func (r *Registry) Statuses() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Status, 0, len(r.scopes))
	for key, sc := range r.scopes {
		selector, _ := r.store.State(key)
		out = append(out, Status{
			Key: key, Kind: sc.desired.Kind, State: sc.state,
			Servable: sc.servable, LastError: sc.lastError, Selector: selector,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// masked shortens a credential for logs, the same way the store does. A scope name is a
// credential, and an SDK key must not reach the logs in full.
func masked(key string) string {
	if len(key) <= 4 {
		return key
	}
	return "..." + key[len(key)-4:]
}
