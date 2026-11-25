package registry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/session"
	"github.com/launchdarkly/ld-relay/v9/internal/megastream/store"
	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
	"github.com/launchdarkly/ld-relay/v9/internal/retry"
)

const base = "payload-1"

// sent is one message a fake sender received.
type sent struct {
	op  string
	key string
}

// fakeSender records what the registry sends, and fails a send when told to. A test can
// set during to act while a send is in progress, when the registry does not hold its lock.
type fakeSender struct {
	sent   []sent
	err    error
	during func(sent)
}

func (f *fakeSender) Register(_ context.Context, key string) error {
	return f.record(sent{op: "add", key: key})
}

func (f *fakeSender) Deregister(_ context.Context, key string) error {
	return f.record(sent{op: "remove", key: key})
}

func (f *fakeSender) record(s sent) error {
	f.sent = append(f.sent, s)
	if f.during != nil {
		f.during(s)
	}
	return f.err
}

// take returns what was sent since the last call.
func (f *fakeSender) take() []sent {
	out := f.sent
	f.sent = nil
	return out
}

// fixture is a registry over a real store, with a clock the test moves and no jitter.
type fixture struct {
	t        *testing.T
	now      time.Time
	objects  *store.Store
	registry *Registry
	sender   *fakeSender
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{
		t:       t,
		now:     time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC),
		objects: store.New(logger),
		sender:  &fakeSender{},
	}
	f.registry = New(logger, f.objects,
		WithClock(func() time.Time { return f.now }),
		WithRetryOptions(retry.WithJitter(func(time.Duration) time.Duration { return 0 })))
	return f
}

// deliver applies messages the way the registry's owner does: the store first, then the
// registry.
func (f *fixture) deliver(msgs ...any) {
	f.t.Helper()
	for i, msg := range msgs {
		require.NoError(f.t, f.objects.Apply(msg), "message %d (%T)", i, msg)
		f.registry.Apply(msg)
	}
}

func (f *fixture) reconcile() []sent {
	f.registry.Reconcile(context.Background(), f.sender)
	return f.sender.take()
}

// connect runs a handshake: the session asks for the hello, then reports the connection
// that presented it.
func (f *fixture) connect(welcome wire.Welcome) []wire.HelloScope {
	f.t.Helper()
	_, scopes := f.registry.Hello()
	presented := make([]string, len(scopes))
	for i, entry := range scopes {
		presented[i] = entry.Key
	}
	f.deliver(session.Connected{Welcome: welcome, Presented: presented})
	return scopes
}

// hydrate opens an unfiltered scope and gives it its first cursor.
func (f *fixture) hydrate(scope, selector string) {
	f.t.Helper()
	f.deliver(intent(scope, false), cursor(scope, selector))
}

func (f *fixture) state(key string) State {
	f.t.Helper()
	for _, status := range f.registry.Statuses() {
		if status.Key == key {
			return status.State
		}
	}
	f.t.Fatalf("no status for %s", key)
	return 0
}

func (f *fixture) known(key string) bool {
	for _, status := range f.registry.Statuses() {
		if status.Key == key {
			return true
		}
	}
	return false
}

func intent(scope string, filtered bool) wire.ServerIntent {
	unfiltered := !filtered
	return wire.ServerIntent{
		T: wire.TypeServerIntent, Scope: scope,
		IntentCode: wire.IntentTransferFull, Unfiltered: &unfiltered,
	}
}

func cursor(scope, selector string) wire.PayloadTransferred {
	return wire.PayloadTransferred{
		T: wire.TypePayloadTransferred, Scope: scope, State: selector, Version: 1,
	}
}

func scopeError(scope, code string) wire.ErrorMessage {
	return wire.ErrorMessage{T: wire.TypeError, Scope: scope, Code: code}
}

func flag(key string) store.ObjectID {
	return store.ObjectID{Kind: store.KindFlag, Key: key}
}

func want(keys ...string) []Desired {
	out := make([]Desired, len(keys))
	for i, key := range keys {
		out[i] = Desired{Key: key}
	}
	return out
}

func added(keys ...string) []sent {
	out := make([]sent, len(keys))
	for i, key := range keys {
		out[i] = sent{op: "add", key: key}
	}
	return out
}

func removed(keys ...string) []sent {
	out := make([]sent, len(keys))
	for i, key := range keys {
		out[i] = sent{op: "remove", key: key}
	}
	return out
}

// The handshake names the root and every wanted credential, each with the selector the store
// may offer. The root is not a scope unless configuration also wants it as one.
func TestHelloPresentsEveryWantedCredentialWithItsSelector(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA", "mob-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")
	f.deliver(session.Disconnected{})

	root, scopes := f.registry.Hello()
	assert.Equal(t, "sdk-env", root)
	assert.Equal(t, []wire.HelloScope{
		{Key: "sdk-viewA", State: "sel-1"},
		{Key: "mob-viewA"},
	}, scopes, "a credential already served comes first")
}

// The session asks for the hello before the consumer has read the previous attempt's
// Disconnected. The presentation therefore takes effect at Connected, and a late
// Disconnected cannot undo it.
func TestAPresentationTakesEffectAtConnected(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")

	// The connection drops and the session redials before the consumer catches up.
	_, scopes := f.registry.Hello()
	require.Len(t, scopes, 1)
	assert.Equal(t, Synced, f.state("sdk-viewA"), "building a hello changes nothing")

	f.deliver(session.Disconnected{})
	assert.Equal(t, Unregistered, f.state("sdk-viewA"))
	f.deliver(session.Connected{Presented: []string{"sdk-viewA"}})
	assert.Equal(t, Presented, f.state("sdk-viewA"))

	assert.Empty(t, f.reconcile(), "a presented credential is not registered a second time")
}

// A credential that appears after the handshake is registered on the live session. The spike
// registered credentials only in the handshake.
func TestACredentialAddedMidSessionIsRegistered(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")
	assert.Empty(t, f.reconcile())

	f.registry.SetDesired("sdk-env", want("sdk-viewA", "mob-viewA"))
	assert.Equal(t, added("mob-viewA"), f.reconcile())
	assert.Equal(t, Presented, f.state("mob-viewA"))
	assert.Empty(t, f.reconcile(), "a credential awaiting its answer is not registered again")

	f.hydrate("mob-viewA", "sel-2")
	_, servable := f.registry.Servable("mob-viewA")
	assert.True(t, servable)
}

// Nothing is sent while disconnected. The next handshake carries the whole desired set.
func TestNothingIsSentWhileDisconnected(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	assert.Empty(t, f.reconcile())

	f.connect(wire.Welcome{})
	f.deliver(session.Disconnected{})
	f.registry.SetDesired("sdk-env", want("sdk-viewA", "mob-viewA"))
	assert.Empty(t, f.reconcile())

	scopes := f.connect(wire.Welcome{})
	assert.Equal(t, []wire.HelloScope{{Key: "mob-viewA"}, {Key: "sdk-viewA"}}, scopes)
}

// A send that fails is retried by the next reconcile, without any recovery of its own.
func TestAFailedRegistrationIsRetried(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")

	f.registry.SetDesired("sdk-env", want("sdk-viewA", "mob-viewA"))
	f.sender.err = errors.New("write failed")
	assert.Equal(t, added("mob-viewA"), f.reconcile())
	assert.Equal(t, Unregistered, f.state("mob-viewA"))

	f.sender.err = nil
	assert.Equal(t, added("mob-viewA"), f.reconcile())
	assert.Equal(t, Presented, f.state("mob-viewA"))
}

// A credential configuration drops is withdrawn and forgotten, and the store discards its
// membership and selector with it.
func TestACredentialNoLongerWantedIsWithdrawn(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA", "mob-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")
	f.hydrate("mob-viewA", "sel-2")

	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	_, servable := f.registry.Servable("mob-viewA")
	assert.False(t, servable, "a credential stops being served once it is not wanted")

	assert.Equal(t, removed("mob-viewA"), f.reconcile())
	assert.False(t, f.known("mob-viewA"))
	assert.NotContains(t, f.objects.Scopes(), "mob-viewA")
	assert.Empty(t, f.reconcile())
}

// A withdrawal waits for the server to answer the presentation. Withdrawing earlier would
// race the answer, and a stale answer could then be applied to a later registration.
func TestAWithdrawalWaitsForTheAnswer(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})

	f.registry.SetDesired("sdk-env", nil)
	assert.Empty(t, f.reconcile(), "the presentation is unanswered")

	f.deliver(intent("sdk-viewA", false))
	assert.Equal(t, removed("sdk-viewA"), f.reconcile())
	assert.False(t, f.known("sdk-viewA"))
}

// A credential that is not on the wire needs no withdrawal.
func TestACredentialOffTheWireIsForgottenWithoutASend(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")
	f.deliver(session.Disconnected{})

	f.registry.SetDesired("sdk-env", nil)
	assert.Empty(t, f.reconcile())
	assert.False(t, f.known("sdk-viewA"))
	assert.NotContains(t, f.objects.Scopes(), "sdk-viewA")
}

// A failed withdrawal is retried, and the credential is forgotten only once it is sent.
func TestAFailedWithdrawalIsRetried(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")

	f.registry.SetDesired("sdk-env", nil)
	f.sender.err = errors.New("write failed")
	assert.Equal(t, removed("sdk-viewA"), f.reconcile())
	assert.True(t, f.known("sdk-viewA"))

	f.sender.err = nil
	assert.Equal(t, removed("sdk-viewA"), f.reconcile())
	assert.False(t, f.known("sdk-viewA"))
}

// A deprecated credential is withdrawn when its expiry passes, whatever the message order.
func TestAnExpiredCredentialIsWithdrawn(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", []Desired{
		{Key: "sdk-new"},
		{Key: "sdk-old", Expires: f.now.Add(time.Hour)},
	})
	f.connect(wire.Welcome{})
	f.hydrate("sdk-new", "sel-1")
	f.hydrate("sdk-old", "sel-2")
	assert.Empty(t, f.reconcile())

	f.now = f.now.Add(time.Hour)
	_, servable := f.registry.Servable("sdk-old")
	assert.False(t, servable)
	assert.Equal(t, removed("sdk-old"), f.reconcile())
	assert.False(t, f.known("sdk-old"))
}

// A rejection destroys the registration: the store discards the membership and the selector,
// the credential stops being served, and it is presented again after the extended backoff.
func TestARejectedCredentialIsDiscardedAndRetriedSlowly(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.deliver(intent("sdk-viewA", true), wire.Ref{
		T: wire.TypeRef, Scope: "sdk-viewA", Kind: store.KindFlag, Key: "f1",
	}, cursor("sdk-viewA", "sel-1"))

	f.deliver(scopeError("sdk-viewA", wire.ErrorScopeRevoked))
	assert.Equal(t, Rejected, f.state("sdk-viewA"))
	_, servable := f.registry.Servable("sdk-viewA")
	assert.False(t, servable)
	assert.Empty(t, f.objects.ScopesFor(flag("f1")), "the membership is discarded")
	_, hasSelector := f.objects.State("sdk-viewA")
	assert.False(t, hasSelector)

	f.now = f.now.Add(retryExtendedInitialDelay - time.Second)
	assert.Empty(t, f.reconcile())
	f.now = f.now.Add(time.Second)
	assert.Equal(t, added("sdk-viewA"), f.reconcile())

	// Each further rejection doubles the wait.
	f.deliver(scopeError("sdk-viewA", wire.ErrorInvalidKey))
	f.now = f.now.Add(2*retryExtendedInitialDelay - time.Second)
	assert.Empty(t, f.reconcile())
	f.now = f.now.Add(time.Second)
	assert.Equal(t, added("sdk-viewA"), f.reconcile())
}

// A refusal keeps the membership, so the credential's SDKs are still served, and discards the
// selector, so the next registration is a full transfer. An outage is retried quickly.
func TestARefusedCredentialKeepsServingAndIsRetried(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.deliver(intent("sdk-viewA", true), wire.Ref{
		T: wire.TypeRef, Scope: "sdk-viewA", Kind: store.KindFlag, Key: "f1",
	}, cursor("sdk-viewA", "sel-1"))

	f.deliver(scopeError("sdk-viewA", wire.ErrorVerificationUnavailable))
	assert.Equal(t, Refused, f.state("sdk-viewA"))
	_, servable := f.registry.Servable("sdk-viewA")
	assert.True(t, servable)
	assert.Equal(t, []string{"sdk-viewA"}, f.objects.ScopesFor(flag("f1")))
	_, hasSelector := f.objects.State("sdk-viewA")
	assert.False(t, hasSelector)

	f.now = f.now.Add(retryInitialDelay - time.Second)
	assert.Empty(t, f.reconcile())
	f.now = f.now.Add(time.Second)
	assert.Equal(t, added("sdk-viewA"), f.reconcile())

	// A cursor ends the failure, so the next refusal starts the curve again.
	f.hydrate("sdk-viewA", "sel-2")
	f.deliver(scopeError("sdk-viewA", wire.ErrorVerificationUnavailable))
	f.now = f.now.Add(retryInitialDelay)
	assert.Equal(t, added("sdk-viewA"), f.reconcile())
}

// A refused credential that configuration drops is withdrawn, which releases its retained
// membership.
func TestARefusedCredentialNoLongerWantedIsWithdrawn(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.deliver(scopeError("sdk-viewA", wire.ErrorScopeRateLimit))

	f.registry.SetDesired("sdk-env", nil)
	assert.Equal(t, removed("sdk-viewA"), f.reconcile())
	assert.False(t, f.known("sdk-viewA"))
}

// An unrecognized code is informational. Guessing that it destroys the scope would
// over-evict.
func TestAnUnrecognizedScopeErrorChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")

	f.deliver(scopeError("sdk-viewA", "some-future-code"))
	assert.Equal(t, Synced, f.state("sdk-viewA"))
	_, hasSelector := f.objects.State("sdk-viewA")
	assert.True(t, hasSelector)
}

// A scope-tagged message for a credential that is not presented is ignored.
func TestMessagesForACredentialNotPresentedAreIgnored(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")
	f.deliver(session.Disconnected{})

	f.registry.Apply(intent("sdk-viewA", false))
	f.registry.Apply(cursor("sdk-viewA", "sel-2"))
	f.registry.Apply(scopeError("sdk-viewA", wire.ErrorInvalidKey))
	assert.Equal(t, Unregistered, f.state("sdk-viewA"))
	_, servable := f.registry.Servable("sdk-viewA")
	assert.True(t, servable)
}

// A disconnect is bounded staleness, so a served credential stays served through it.
func TestADisconnectKeepsServability(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", []Desired{{Key: "mob-viewA", Kind: KindMobile}})
	f.connect(wire.Welcome{})
	_, servable := f.registry.Servable("mob-viewA")
	assert.False(t, servable, "nothing is served before the first cursor")
	f.hydrate("mob-viewA", "sel-1")

	f.deliver(session.Disconnected{})
	kind, servable := f.registry.Servable("mob-viewA")
	assert.True(t, servable)
	assert.Equal(t, KindMobile, kind)
}

// The root's revocation is the destruction signal for the scope that shares its credential.
func TestTheRootsRevocationDestroysItsScope(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-env", "sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-env", "sel-1")
	f.hydrate("sdk-viewA", "sel-2")

	f.deliver(wire.Goodbye{T: wire.TypeGoodbye, Code: wire.GoodbyeRootRevoked})
	assert.Equal(t, Rejected, f.state("sdk-env"))
	assert.NotContains(t, f.objects.Scopes(), "sdk-env")
	_, servable := f.registry.Servable("sdk-env")
	assert.False(t, servable)
	_, servable = f.registry.Servable("sdk-viewA")
	assert.True(t, servable, "a sibling scope is untouched")
}

// With more credentials than the advertised bound, the handshake presents those already
// served first, then the rest in key order, so the choice is stable across connections.
func TestHelloHonorsTheAdvertisedScopeBound(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-c"))
	f.connect(wire.Welcome{MaxScopes: 2})
	f.hydrate("sdk-c", "sel-1")
	f.deliver(session.Disconnected{})

	f.registry.SetDesired("sdk-env", want("sdk-a", "sdk-b", "sdk-c"))
	_, scopes := f.registry.Hello()
	assert.Equal(t, []wire.HelloScope{{Key: "sdk-c", State: "sel-1"}, {Key: "sdk-a"}}, scopes)
}

// A registration on the live session respects the bound too. A withdrawal frees its slot in
// the same reconcile: the server processes the relay's messages in order, so it sees the
// removal before the registration that takes the slot.
func TestRegistrationsRespectTheScopeBound(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-a"))
	f.connect(wire.Welcome{MaxScopes: 2})
	f.hydrate("sdk-a", "sel-1")

	f.registry.SetDesired("sdk-env", want("sdk-a", "sdk-b", "sdk-c"))
	assert.Equal(t, added("sdk-b"), f.reconcile())
	assert.Empty(t, f.reconcile(), "the bound is full")

	f.registry.SetDesired("sdk-env", want("sdk-b", "sdk-c"))
	f.hydrate("sdk-b", "sel-2")
	assert.Equal(t, append(removed("sdk-a"), added("sdk-c")...), f.reconcile())
}

// Configuration can drop a credential after the hello names it and before the connection is
// applied. The server may hold the presentation and resume it from the selector the hello
// offered, so the registry keeps the credential and its store state, and withdraws it once
// the server answers.
func TestACredentialDroppedBeforeItsConnectionIsAppliedIsWithdrawn(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")
	f.deliver(session.Disconnected{})

	_, scopes := f.registry.Hello()
	require.Equal(t, []wire.HelloScope{{Key: "sdk-viewA", State: "sel-1"}}, scopes)

	f.registry.SetDesired("sdk-env", nil)
	assert.Empty(t, f.reconcile(), "nothing is on the wire before the connection is applied")
	assert.True(t, f.known("sdk-viewA"), "the hello named it")

	f.deliver(session.Connected{Welcome: wire.Welcome{}, Presented: []string{"sdk-viewA"}})
	assert.Equal(t, Presented, f.state("sdk-viewA"))
	_, servable := f.registry.Servable("sdk-viewA")
	assert.False(t, servable)
	assert.Empty(t, f.reconcile(), "the presentation is unanswered")

	unfiltered := true
	f.deliver(wire.ServerIntent{
		T: wire.TypeServerIntent, Scope: "sdk-viewA",
		IntentCode: wire.IntentTransferChanges, Unfiltered: &unfiltered,
	})
	assert.Equal(t, removed("sdk-viewA"), f.reconcile())
	assert.False(t, f.known("sdk-viewA"))
	assert.NotContains(t, f.objects.Scopes(), "sdk-viewA")
}

// A credential that a hello named is forgotten once a connection that did not present it is
// applied.
func TestACredentialAHelloNamedIsForgottenWhenTheConnectionOmitsIt(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.registry.Hello()
	f.registry.SetDesired("sdk-env", nil)
	assert.Empty(t, f.reconcile())
	assert.True(t, f.known("sdk-viewA"))

	f.connect(wire.Welcome{})
	assert.Empty(t, f.reconcile())
	assert.False(t, f.known("sdk-viewA"))
}

// A presentation that configuration dropped holds its slot until the server answers it, so a
// replacement waits for the withdrawal.
func TestAnUnansweredDroppedPresentationHoldsItsSlot(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-a"))
	f.connect(wire.Welcome{MaxScopes: 1})

	f.registry.SetDesired("sdk-env", want("sdk-b"))
	assert.Empty(t, f.reconcile(), "sdk-a still holds the only slot")

	f.deliver(intent("sdk-a", false))
	assert.Equal(t, append(removed("sdk-a"), added("sdk-b")...), f.reconcile())
}

// Configuration can want a credential again while its withdrawal is sent. The server drops
// the registration, so the registry registers the credential again with a full transfer.
func TestACredentialWantedAgainDuringItsWithdrawalIsRegisteredAgain(t *testing.T) {
	f := newFixture(t)
	f.registry.SetDesired("sdk-env", want("sdk-viewA"))
	f.connect(wire.Welcome{})
	f.hydrate("sdk-viewA", "sel-1")

	f.registry.SetDesired("sdk-env", nil)
	f.sender.during = func(s sent) {
		if s.op == "remove" {
			f.registry.SetDesired("sdk-env", want("sdk-viewA"))
		}
	}
	assert.Equal(t, removed("sdk-viewA"), f.reconcile())
	f.sender.during = nil

	assert.True(t, f.known("sdk-viewA"))
	assert.Equal(t, Unregistered, f.state("sdk-viewA"))
	assert.NotContains(t, f.objects.Scopes(), "sdk-viewA")
	_, servable := f.registry.Servable("sdk-viewA")
	assert.False(t, servable, "the withdrawn data is gone")

	assert.Equal(t, added("sdk-viewA"), f.reconcile())
}
