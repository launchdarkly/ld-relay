// Package concurrency provides an admission limiter that bounds how much concurrent
// work a burst of requests or connections can impose on Relay. It uses two limits: a
// maximum number of slots held at once, and a bounded FIFO queue of callers waiting for
// a slot.
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
	// MaxQueued is the number of callers that may wait in FIFO order for a slot once all
	// slots are held. A value of 0 rejects callers immediately instead of waiting.
	MaxQueued int
}

// Stats is a point-in-time snapshot of a Limiter's counters for logging and metrics.
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
// a bounded FIFO queue of waiters. The zero value is not usable; construct one with New.
type Limiter struct {
	name    string
	enabled bool

	tokens    chan struct{} // holds the free slots; receive to acquire one, send to release one
	maxQueued int64
	waiting   int64
	shutdown  chan struct{}
	closeOnce sync.Once

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
	l.maxQueued = int64(p.MaxQueued)
	l.shutdown = make(chan struct{})
	return l
}

// Name returns the limiter's identifier.
func (l *Limiter) Name() string { return l.name }

// Enabled reports whether the limiter is enforcing a limit.
func (l *Limiter) Enabled() bool { return l != nil && l.enabled }

// Acquire attempts to admit one unit of work. On success it returns a release function,
// which the caller must call exactly once, and ok is true. It returns a no-op release and
// ok=false if the queue is full, ctx is cancelled, or the limiter is shut down. A disabled
// or nil limiter always admits immediately.
func (l *Limiter) Acquire(ctx context.Context) (release func(), ok bool) {
	if !l.Enabled() {
		return func() {}, true
	}

	// Take a free slot if one is available.
	select {
	case <-l.tokens:
		return l.admit(), true
	default:
	}

	// No free slot. Join the bounded queue, or reject if it is full.
	if atomic.AddInt64(&l.waiting, 1) > l.maxQueued {
		atomic.AddInt64(&l.waiting, -1)
		l.rejected.Add(1)
		return func() {}, false
	}
	defer atomic.AddInt64(&l.waiting, -1)

	select {
	case <-l.tokens:
		return l.admit(), true
	case <-ctx.Done():
		l.rejected.Add(1)
		return func() {}, false
	case <-l.shutdown:
		l.rejected.Add(1)
		return func() {}, false
	}
}

func (l *Limiter) admit() func() {
	l.admitted.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() { l.tokens <- struct{}{} })
	}
}

// Close unblocks all waiters, which then receive ok=false. It may be called more than once.
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
