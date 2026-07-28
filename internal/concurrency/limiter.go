// Package concurrency provides an admission/concurrency limiter used to bound
// how much work a request or connection herd can impose on Relay. It implements
// a two-limit model (max concurrently-held tokens + a bounded FIFO wait backlog)
// with an optional per-environment fairness gate, modeled on the streamer
// (fdcore) admission.Limiter.
package concurrency

import (
	"context"
	"sync"
	"sync/atomic"
)

// Params configures a Limiter. A MaxConcurrent <= 0 yields a disabled limiter
// whose Acquire is a zero-cost pass-through, so the feature is fully opt-in.
type Params struct {
	// MaxConcurrent is the number of tokens that may be held at once.
	MaxConcurrent int
	// MaxQueued is the number of callers that may wait (FIFO) for a token when
	// all are held. 0 means never wait — reject as soon as concurrency is full.
	MaxQueued int
	// PerEnvMax caps how many of a single environment's callers may participate
	// (held + waiting) in the limiter at once. 0 disables per-environment
	// fairness (global-only). The per-environment gate is always try-only.
	PerEnvMax int
}

// Stats is a point-in-time snapshot of a Limiter's counters, for logging/metrics.
type Stats struct {
	Enabled       bool
	MaxConcurrent int
	MaxQueued     int
	Held          int
	Waiting       int
	Admitted      int64
	Rejected      int64
}

// Limiter bounds concurrency using a two-limit global semaphore plus an optional
// per-environment try-only gate. The zero value is not usable; construct with New.
type Limiter struct {
	name    string
	enabled bool

	tokens    chan struct{} // holds MaxConcurrent slots; receive to acquire, send to release
	maxQueued int64
	waiting   int64
	shutdown  chan struct{}
	closeOnce sync.Once

	perEnvMax int
	perEnv    sync.Map // envKey -> *envGate

	admitted atomic.Int64
	rejected atomic.Int64
}

type envGate struct {
	slots chan struct{} // cap = perEnvMax; try-receive to enter, send to leave
}

// New builds a Limiter. name is used only for logging/metrics identification.
func New(name string, p Params) *Limiter {
	l := &Limiter{name: name}
	if p.MaxConcurrent <= 0 {
		return l // disabled
	}
	l.enabled = true
	l.tokens = make(chan struct{}, p.MaxConcurrent)
	for i := 0; i < p.MaxConcurrent; i++ {
		l.tokens <- struct{}{}
	}
	l.maxQueued = int64(p.MaxQueued)
	l.shutdown = make(chan struct{})
	if p.PerEnvMax > 0 {
		l.perEnvMax = p.PerEnvMax
	}
	return l
}

// Name returns the limiter's identifier.
func (l *Limiter) Name() string { return l.name }

// Enabled reports whether the limiter is enforcing a limit.
func (l *Limiter) Enabled() bool { return l != nil && l.enabled }

// Acquire attempts to admit one unit of work for the given environment key.
// On success it returns a release func (call exactly once) and ok=true. If the
// per-env gate or the global backlog is full, or ctx is cancelled, or the
// limiter is shut down, it returns a no-op release and ok=false. A disabled or
// nil limiter always admits immediately.
func (l *Limiter) Acquire(ctx context.Context, envKey string) (release func(), ok bool) {
	if !l.Enabled() {
		return func() {}, true
	}

	// Per-environment try-only gate: bounds one env's participants (held+waiting).
	var releaseEnv func()
	if l.perEnvMax > 0 {
		g := l.gateFor(envKey)
		select {
		case g.slots <- struct{}{}:
			releaseEnv = func() { <-g.slots }
		default:
			l.rejected.Add(1)
			return func() {}, false
		}
	}

	reject := func() (func(), bool) {
		if releaseEnv != nil {
			releaseEnv()
		}
		l.rejected.Add(1)
		return func() {}, false
	}

	// Fast path: a token is immediately available.
	select {
	case <-l.tokens:
		return l.admit(releaseEnv), true
	default:
	}

	// No token free: enter the bounded FIFO backlog, or reject.
	if atomic.AddInt64(&l.waiting, 1) > l.maxQueued {
		atomic.AddInt64(&l.waiting, -1)
		return reject()
	}
	defer atomic.AddInt64(&l.waiting, -1)

	select {
	case <-l.tokens:
		return l.admit(releaseEnv), true
	case <-ctx.Done():
		return reject()
	case <-l.shutdown:
		return reject()
	}
}

func (l *Limiter) admit(releaseEnv func()) func() {
	l.admitted.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			l.tokens <- struct{}{}
			if releaseEnv != nil {
				releaseEnv()
			}
		})
	}
}

func (l *Limiter) gateFor(envKey string) *envGate {
	if g, ok := l.perEnv.Load(envKey); ok {
		return g.(*envGate)
	}
	g := &envGate{slots: make(chan struct{}, l.perEnvMax)}
	actual, _ := l.perEnv.LoadOrStore(envKey, g)
	return actual.(*envGate)
}

// Close unblocks all waiters (they receive ok=false). Idempotent.
func (l *Limiter) Close() {
	if !l.Enabled() {
		return
	}
	l.closeOnce.Do(func() { close(l.shutdown) })
}

// Stats snapshots the limiter's counters.
func (l *Limiter) Stats() Stats {
	if !l.Enabled() {
		return Stats{Enabled: false}
	}
	return Stats{
		Enabled:       true,
		MaxConcurrent: cap(l.tokens),
		MaxQueued:     int(l.maxQueued),
		Held:          cap(l.tokens) - len(l.tokens),
		Waiting:       int(atomic.LoadInt64(&l.waiting)),
		Admitted:      l.admitted.Load(),
		Rejected:      l.rejected.Load(),
	}
}
