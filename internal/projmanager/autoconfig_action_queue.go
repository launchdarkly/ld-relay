package projmanager

import (
	"sync"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

var _ AutoConfigActions = &AutoConfigActionQueue{}

const (
	// defaultMaxConcurrentActions bounds how many environment actions execute at once. See
	// AutoConfigActionQueue.execute for why the bound exists; it is set well above any realistic
	// environment count for a single account so that ordinary rotations still overlap freely.
	defaultMaxConcurrentActions = 32

	// defaultCloseGracePeriod is how long Close waits for queued actions to finish. Waiting
	// indefinitely would hand back the shutdown delay this type exists to remove: an environment
	// whose anchor build is stalled holds its action for up to Main.InitTimeout (10s by default),
	// and the process should not sit on that. Abandoning in-flight actions is safe -- see Close.
	defaultCloseGracePeriod = 2 * time.Second
)

// envKey identifies one relay environment for queueing purposes. The filter key is part of
// the identity, not decoration: EnvironmentManager turns one upstream environment into one default
// plus one per configured filter (see EnvironmentParams.WithFilter), and each of those is a separate
// EnvContext with its own SDK client and its own anchor. They must not wait behind each other.
type envKey struct {
	envID  config.EnvironmentID
	filter config.FilterKey
}

func keyForParams(params envfactory.EnvironmentParams) envKey {
	return envKey{envID: params.EnvID, filter: params.Identifiers.FilterKey}
}

// envQueue holds the actions still to run for one environment, oldest first. It carries no lock of
// its own: AutoConfigActionQueue.mu guards every access, which is what makes the retire-vs-enqueue
// handoff in drain race-free.
type envQueue struct {
	pending []func()
}

// AutoConfigActionQueue wraps an AutoConfigActions so that each environment's actions run on their
// own goroutine, in submission order, independently of every other environment's.
//
// Why this exists: StreamManager consumes the auto-configuration SSE stream on a single goroutine
// and ran each environment's action to completion inline. An environment whose SDK anchor moves
// rebuilds its upstream client synchronously (relayenv's reanchor -> buildNewAnchorClient), blocking
// for up to Main.InitTimeout. Every other environment's adds, updates, and credential revocations
// queued behind it, so a bulk rotation across N environments cost N * InitTimeout in the worst case
// and a revocation could sit unapplied for minutes. One queue per environment makes that cost the
// slowest single build rather than the sum of all of them.
//
// What is deliberately preserved:
//
//   - Per-environment ordering. Actions for one environment share a queue, so an add followed by a
//     delete for that environment can never reorder. Only distinct environments overlap.
//   - StreamManager's single-message-at-a-time invariant. Only the handler call is deferred; event
//     parsing, version bookkeeping (envReceiver.Upsert) and cache writes still happen on the stream
//     goroutine before anything is queued here.
//   - The re-anchor sequence itself. Nothing in relayenv changes: within an environment the
//     reconcile is still fully synchronous, so its commit/rollback atomicity holds as before.
//
// Running distinct environments concurrently is safe because the relay-wide state they touch is
// already synchronized -- Relay.addEnvironment holds Relay.lock, and EnvironmentLookup has its own
// mutex -- and because each environment already runs a credential-expiry ticker goroutine that
// mutates that same state concurrently with the stream goroutine. This extends a concurrency class
// the design already accommodates rather than introducing a new one.
type AutoConfigActionQueue struct {
	next        AutoConfigActions
	loggers     ldlog.Loggers
	sem         chan struct{}
	closeGrace  time.Duration
	mu          sync.Mutex
	queues      map[envKey]*envQueue
	closed      bool
	outstanding sync.WaitGroup
}

// NewAutoConfigActionQueue wraps next so that environments are processed independently of one
// another. The caller must Close it once no further actions can arrive.
func NewAutoConfigActionQueue(next AutoConfigActions, loggers ldlog.Loggers) *AutoConfigActionQueue {
	return newAutoConfigActionQueue(next, loggers, defaultMaxConcurrentActions, defaultCloseGracePeriod)
}

func newAutoConfigActionQueue(
	next AutoConfigActions,
	loggers ldlog.Loggers,
	maxConcurrent int,
	closeGrace time.Duration,
) *AutoConfigActionQueue {
	loggers.SetPrefix("[AutoConfigActionQueue]")
	return &AutoConfigActionQueue{
		next:       next,
		loggers:    loggers,
		sem:        make(chan struct{}, maxConcurrent),
		closeGrace: closeGrace,
		queues:     make(map[envKey]*envQueue),
	}
}

// The three environment methods share one shape: work out which environment the call concerns, then
// queue the same call against the next handler. Only the queue key differs.
func (q *AutoConfigActionQueue) AddEnvironment(params envfactory.EnvironmentParams) {
	q.enqueue(keyForParams(params), func() { q.next.AddEnvironment(params) })
}

func (q *AutoConfigActionQueue) UpdateEnvironment(params envfactory.EnvironmentParams) {
	q.enqueue(keyForParams(params), func() { q.next.UpdateEnvironment(params) })
}

func (q *AutoConfigActionQueue) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	q.enqueue(envKey{envID: id, filter: filter}, func() { q.next.DeleteEnvironment(id, filter) })
}

// ReceivedAllEnvironments forwards to the wrapped handler only once every action queued since the
// previous call has run. That keeps the downstream meaning of the signal intact: it sets Relay's
// fullyConfigured flag, which gates both request serving (getEnvironment returns errRelayNotReady
// until it is set) and the reported status health, so it must not fire while environments from the
// payload are still being applied.
//
// It returns immediately and waits on its own goroutine. Blocking the caller would hand head-of-line
// blocking back for "put" payloads specifically, which is the case this type is most needed for.
//
// Note this makes readiness slightly stricter than it was: previously the flag was set as soon as
// the payload had been walked, which on a first "put" is before any environment's client has been
// built. It now additionally waits for each environment's action to have run.
func (q *AutoConfigActionQueue) ReceivedAllEnvironments() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	// Wait on every environment that still has work outstanding. A queue leaves q.queues only after
	// its worker has run everything in it -- drain pops an action, runs it, and only deletes the queue
	// on a later pass that finds it empty -- so an environment absent here is already fully applied
	// and needs no barrier.
	pending := make([]envKey, 0, len(q.queues))
	for key := range q.queues {
		pending = append(pending, key)
	}
	q.mu.Unlock()

	var drained sync.WaitGroup
	drained.Add(len(pending))
	for _, key := range pending {
		if !q.enqueue(key, drained.Done) {
			// Closed underneath us; nothing will run this barrier, so release it here rather than
			// leaving the goroutine below parked forever.
			drained.Done()
		}
	}

	q.outstanding.Go(func() {
		drained.Wait()
		q.mu.Lock()
		closed := q.closed
		q.mu.Unlock()
		if !closed {
			q.next.ReceivedAllEnvironments()
		}
	})
}

// Close stops accepting new actions and waits up to the grace period for queued ones to finish.
//
// Actions still running when the grace period expires are abandoned rather than waited on, which is
// safe because every one of them is already guarded against a closing Relay: addEnvironment refuses
// once Relay is closed, EnvContext.Close is idempotent, and the re-anchor sequence re-checks the
// environment's closed flag after its build and declines to commit into a closed environment.
func (q *AutoConfigActionQueue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.outstanding.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(q.closeGrace):
		q.loggers.Warnf("Timed out after %v waiting for in-flight environment actions to finish; abandoning them", q.closeGrace)
	}
}

// enqueue appends fn to key's queue, starting a worker for that environment if one is not already
// running. It reports whether the action was accepted; it is refused only after Close. It never
// blocks on the action itself, so the caller (the stream goroutine) is never delayed by one
// environment's work.
func (q *AutoConfigActionQueue) enqueue(key envKey, fn func()) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return false
	}
	envq := q.queues[key]
	if envq == nil {
		envq = &envQueue{}
		q.queues[key] = envq
		q.outstanding.Go(func() { q.drain(key, envq) })
	}
	envq.pending = append(envq.pending, fn)
	return true
}

// drain runs key's actions one at a time until the queue empties, then retires it. A worker per
// non-empty queue (rather than one per environment for the environment's whole life) means a
// deleted or idle environment leaves nothing behind to leak.
func (q *AutoConfigActionQueue) drain(key envKey, envq *envQueue) {
	for {
		fn, ok := q.nextAction(key, envq)
		if !ok {
			return
		}
		q.execute(fn)
	}
}

// nextAction takes the oldest action off key's queue. When the queue is empty it retires the queue
// instead and reports false, which ends the worker.
//
// Deciding between those two outcomes under the same lock that enqueue uses to look queues up is what
// makes the handoff safe. A concurrent enqueue either finds this queue and appends -- so the emptiness
// check below sees the new action -- or it creates a fresh queue after the delete. An action can never
// land on a retired queue, and two workers can never run for one environment.
func (q *AutoConfigActionQueue) nextAction(key envKey, envq *envQueue) (func(), bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(envq.pending) == 0 {
		if q.queues[key] == envq {
			delete(q.queues, key)
		}
		return nil, false
	}
	fn := envq.pending[0]
	envq.pending[0] = nil // release the closure; the slice header still points past it
	envq.pending = envq.pending[1:]
	return fn, true
}

// execute runs a single action under the concurrency bound.
//
// The bound exists because a "put" that adds every environment at once would otherwise construct
// them all in parallel -- each one configuring a data store and registering metrics -- where
// previously they were built one at a time on the stream goroutine. It also caps the goroutines an
// account with very many environments can put in flight at once.
//
// A panicking action is contained rather than allowed to kill the worker: an escaped panic would
// leave the queue in the map with nothing draining it, silently freezing that environment's
// configuration for the life of the process.
func (q *AutoConfigActionQueue) execute(fn func()) {
	q.sem <- struct{}{}
	defer func() {
		<-q.sem
		if p := recover(); p != nil {
			q.loggers.Errorf("Panic while applying an auto-configuration action: %+v", p)
		}
	}()
	fn()
}
