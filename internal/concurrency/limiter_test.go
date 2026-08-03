package concurrency

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"
)

// waitForWaiting polls until the limiter reports the given queue depth, so tests can
// establish "a caller is parked" without a fixed sleep.
func waitForWaiting(t *testing.T, l *Limiter, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for l.Stats().Waiting != n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Waiting==%d (stats: %+v)", n, l.Stats())
		}
		runtime.Gosched()
	}
}

func TestDisabledLimiterAlwaysAdmits(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 0})
	if l.Enabled() {
		t.Fatal("expected disabled")
	}
	for i := 0; i < 100; i++ {
		release, ok := l.Acquire(context.Background())
		if !ok {
			t.Fatal("disabled limiter must always admit")
		}
		release()
	}
}

func TestNilLimiterAdmits(t *testing.T) {
	var l *Limiter
	release, ok := l.Acquire(context.Background())
	if !ok {
		t.Fatal("nil limiter must admit")
	}
	release()
	if l.Name() != "" {
		t.Fatal("nil limiter Name should be empty")
	}
	l.Close() // must not panic
}

func TestRejectWhenNoBacklog(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 2, MaxQueued: 0})
	r1, ok1 := l.Acquire(context.Background())
	r2, ok2 := l.Acquire(context.Background())
	_, ok3 := l.Acquire(context.Background())
	if !ok1 || !ok2 {
		t.Fatal("first two should be admitted")
	}
	if ok3 {
		t.Fatal("third should be rejected (no backlog)")
	}
	r1()
	r2()
	if s := l.Stats(); s.Admitted != 2 || s.Rejected != 1 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestQueueThenAdmitOnRelease(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	r1, ok1 := l.Acquire(context.Background())
	if !ok1 {
		t.Fatal("first should be admitted")
	}
	admitted := make(chan struct{})
	go func() {
		r2, ok2 := l.Acquire(context.Background()) // must queue, then admit when r1 releases
		if ok2 {
			close(admitted)
			r2()
		}
	}()
	waitForWaiting(t, l, 1)
	select {
	case <-admitted:
		t.Fatal("queued caller admitted before release")
	default:
	}
	r1()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("queued caller not admitted after release")
	}
}

func TestBacklogFullRejects(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	r1, _ := l.Acquire(context.Background()) // holds the only token
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if r, ok := l.Acquire(waiterCtx); ok { // fills the single backlog slot
			r()
		}
	}()
	waitForWaiting(t, l, 1)
	if _, ok := l.Acquire(context.Background()); ok {
		t.Fatal("expected rejection when backlog is full")
	}
	// Unwind without leaking the waiter or its token.
	cancelWaiter()
	wg.Wait()
	r1()
}

func TestWaitingCountsOnlyParkedCallers(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	r1, _ := l.Acquire(context.Background())
	admitted := make(chan func())
	go func() {
		if r, ok := l.Acquire(context.Background()); ok {
			admitted <- r
		}
	}()
	waitForWaiting(t, l, 1)
	r1() // hand the slot to the waiter
	r2 := <-admitted
	// An admitted caller must no longer count against the queue bound: with the single
	// backlog slot logically empty, a new caller must be able to queue rather than be shed.
	waitForWaiting(t, l, 0)
	queued := make(chan bool)
	go func() {
		r, ok := l.Acquire(context.Background())
		if ok {
			r()
		}
		queued <- ok
	}()
	waitForWaiting(t, l, 1)
	r2()
	if ok := <-queued; !ok {
		t.Fatal("caller was shed while the queue was logically empty")
	}
}

func TestContextCancelUnblocksWaiter(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 5})
	r1, _ := l.Acquire(context.Background())
	defer r1()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)
	go func() {
		_, ok := l.Acquire(ctx)
		done <- ok
	}()
	waitForWaiting(t, l, 1)
	before := l.Stats().Rejected
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("expected rejection on context cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not unblocked by context cancel")
	}
	if got := l.Stats().Rejected; got != before+1 {
		t.Fatalf("cancelled waiter must count as rejected: before=%d after=%d", before, got)
	}
}

func TestAlreadyCancelledContextIsRejected(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Even with a slot free, an abandoned request must not consume it.
	if _, ok := l.Acquire(ctx); ok {
		t.Fatal("an already-cancelled request must be rejected")
	}
	if s := l.Stats(); s.Held != 0 || s.Rejected != 1 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestCloseIsAnAdmissionBarrier(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 2, MaxQueued: 2})
	l.Close()
	// All slots are free, but a closed limiter must not admit new work.
	for i := 0; i < 4; i++ {
		if _, ok := l.Acquire(context.Background()); ok {
			t.Fatal("Acquire after Close must be rejected")
		}
	}
	if s := l.Stats(); s.Rejected != 4 || s.Admitted != 0 {
		t.Fatalf("unexpected stats: %+v", s)
	}
}

func TestCloseUnblocksWaiters(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 4})
	r1, _ := l.Acquire(context.Background())
	results := make(chan bool, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, ok := l.Acquire(context.Background())
			results <- ok
		}()
	}
	waitForWaiting(t, l, 3)
	l.Close()
	for i := 0; i < 3; i++ {
		select {
		case ok := <-results:
			if ok {
				t.Fatal("waiter admitted by Close")
			}
		case <-time.After(time.Second):
			t.Fatal("waiter not unblocked by Close")
		}
	}
	r1()      // releasing a held slot after Close must not panic or block
	l.Close() // idempotent
}

func TestReleaseIsIdempotent(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 0})
	r, _ := l.Acquire(context.Background())
	r()
	r() // must not release a second token
	// Two acquires should now succeed sequentially, proving only one token exists.
	r1, ok1 := l.Acquire(context.Background())
	if !ok1 {
		t.Fatal("expected admit after release")
	}
	if _, ok2 := l.Acquire(context.Background()); ok2 {
		t.Fatal("double release leaked a token")
	}
	r1()
}

func TestStatsSnapshot(t *testing.T) {
	l := New("gate", Params{MaxConcurrent: 2, MaxQueued: 3})
	if l.Name() != "gate" {
		t.Fatalf("unexpected name %q", l.Name())
	}
	r1, _ := l.Acquire(context.Background())
	r2, _ := l.Acquire(context.Background())
	unblocked := make(chan struct{})
	go func() {
		defer close(unblocked)
		if r, ok := l.Acquire(context.Background()); ok {
			r()
		}
	}()
	waitForWaiting(t, l, 1)

	s := l.Stats()
	if !s.Enabled || s.MaxConcurrent != 2 || s.MaxQueued != 3 || s.Held != 2 || s.Waiting != 1 || s.Admitted != 2 {
		t.Fatalf("unexpected stats: %+v", s)
	}
	l.Close() // unblock the waiter
	<-unblocked
	r1()
	r2()
	if s := l.Stats(); s.Held != 0 {
		t.Fatalf("expected all slots free after release: %+v", s)
	}

	if ds := (&Limiter{}).Stats(); ds.Enabled {
		t.Fatal("disabled limiter must report Enabled=false")
	}
}

func TestConcurrentAcquireBoundsHeld(t *testing.T) {
	const maxC = 4
	l := New("t", Params{MaxConcurrent: maxC, MaxQueued: 1000})
	var mu sync.Mutex
	held, maxObserved := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := l.Acquire(context.Background())
			if !ok {
				return
			}
			mu.Lock()
			held++
			if held > maxObserved {
				maxObserved = held
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			held--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
	if maxObserved > maxC {
		t.Fatalf("held %d exceeded MaxConcurrent %d", maxObserved, maxC)
	}
}
