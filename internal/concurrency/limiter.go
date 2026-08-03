// Package concurrency provides an admission limiter that bounds how much concurrent
// work a burst of requests or connections can impose on Relay. It uses two limits: a
// maximum number of slots held at once, and a bounded queue of callers waiting for a
// slot in approximate arrival order.
package concurrency

import (
	"context"
	"sync"
	"sync/atomic"
)

// Params configures a Limiter. A MaxConcurrent of 0 or less produces a disabled limiter
// whose Acquire always admits and does nothing.
type Params struct {
	// MaxConcurrent is the number of slots that may be held at once.
	MaxConcurrent int
	// MaxQueued is how many callers may wait for a slot once every slot is held, served
	// in approximate arrival order. A value of 0 or less adds no waiting capacity: when
	// every slot is held a caller is rejected immediately, though a caller arriving just
	// as a slot is being released may briefly wait to take it.
	MaxQueued int
}

// Stats is a point-in-time snapshot of a Limiter's counters for logging and metrics.
// Held is exact; Waiting is derived from the occupancy counter and may transiently count
// a caller whose slot handoff is in progress.
type Stats struct {
	Enabled       bool
	MaxConcurrent int
	MaxQueued     int
	Held          int
	Waiting       int
	Admitted      int64
	Rejected      int64
}

// Limiter bounds concurrency with two limits: a maximum number of slots held at once and
// a bounded queue of waiters served in approximate arrival order. The zero value is not
// usable; construct one with New.
type Limiter struct {
	name    string
	enabled bool

	tokens    chan struct{} // holds the free slots; receive to acquire one, send to release one
	maxQueued int
	// inFlight counts the callers occupying the budget: slot holders plus queued waiters,
	// bounded by MaxConcurrent+MaxQueued. A caller reserves its unit once on entry and
	// keeps it from queue to held to released, so handing a released slot to a parked
	// waiter never leaves a moment where the waiter still counts against the queue while
	// the slot is already spoken for. A separate queue counter has exactly that moment (it
	// lasts until the woken goroutine is scheduled) and sheds an admissible caller on
	// every slot turnover under burst load.
	inFlight     atomic.Int64
	maxOccupancy int64
	shutdown     chan struct{}
	closeOnce    sync.Once

	admitted atomic.Int64
	rejected atomic.Int64
}

// New builds a Limiter. name identifies the limiter in logs and metrics.
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
	l.maxQueued = max(p.MaxQueued, 0)
	l.maxOccupancy = int64(p.MaxConcurrent) + int64(l.maxQueued)
	l.shutdown = make(chan struct{})
	return l
}

// Name returns the limiter's identifier, or an empty string for a nil limiter.
func (l *Limiter) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Enabled reports whether the limiter is enforcing a limit.
func (l *Limiter) Enabled() bool { return l != nil && l.enabled }

// Acquire attempts to admit one unit of work. On success it returns a release function,
// which the caller must call exactly once, and ok is true. It returns a no-op release and
// ok=false if the queue is full, ctx is already cancelled or becomes cancelled while
// waiting, or the limiter is shut down. A disabled or nil limiter always admits
// immediately.
func (l *Limiter) Acquire(ctx context.Context) (release func(), ok bool) {
	if !l.Enabled() {
		return func() {}, true
	}

	// Refuse work that arrives after shutdown or is already abandoned, even when a slot
	// is free: Close is an admission barrier, and a dead request would only waste the slot.
	select {
	case <-l.shutdown:
		return l.reject()
	default:
	}
	if ctx.Err() != nil {
		return l.reject()
	}

	// Reserve one unit of the budget's total capacity (slots plus queue). Rejecting on
	// overflow here is what bounds the queue.
	if l.inFlight.Add(1) > l.maxOccupancy {
		l.inFlight.Add(-1)
		return l.reject()
	}

	// Take a free slot if one is available.
	select {
	case <-l.tokens:
		return l.admit()
	default:
	}

	// Every slot is held. Wait for one; the occupancy reservation above bounds how many
	// callers may wait here.
	select {
	case <-l.tokens:
		return l.admit()
	case <-ctx.Done():
		l.inFlight.Add(-1)
		return l.reject()
	case <-l.shutdown:
		l.inFlight.Add(-1)
		return l.reject()
	}
}

// admit completes an admission after a slot has been received -- unless shutdown has
// landed in the meantime, in which case the slot is returned and the caller is rejected,
// so a slot released concurrently with Close cannot smuggle a waiter past it.
func (l *Limiter) admit() (func(), bool) {
	select {
	case <-l.shutdown:
		l.tokens <- struct{}{} // never blocks: this slot's buffer capacity is unoccupied
		l.inFlight.Add(-1)
		return l.reject()
	default:
	}
	l.admitted.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			// Free the occupancy before returning the slot, so the queue capacity this
			// release re-opens is visible to arrivals no later than the slot itself.
			l.inFlight.Add(-1)
			l.tokens <- struct{}{}
		})
	}, true
}

func (l *Limiter) reject() (func(), bool) {
	l.rejected.Add(1)
	return func() {}, false
}

// Close stops admissions: once it returns, callers entering Acquire are rejected, and
// parked waiters are unblocked and rejected. A waiter handed a slot concurrently with
// Close re-checks shutdown and returns the slot instead of keeping it; only an admission
// that fully completed before shutdown landed keeps its slot. Releases of held slots
// remain safe afterwards. It may be called more than once.
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
	held := cap(l.tokens) - len(l.tokens)
	waiting := max(int(l.inFlight.Load())-held, 0)
	return Stats{
		Enabled:       true,
		MaxConcurrent: cap(l.tokens),
		MaxQueued:     l.maxQueued,
		Held:          held,
		Waiting:       waiting,
		Admitted:      l.admitted.Load(),
		Rejected:      l.rejected.Load(),
	}
}
