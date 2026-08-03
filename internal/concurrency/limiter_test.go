package concurrency

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitForWaiting polls until Stats reports n waiting callers. The count is incremented one
// statement before the caller parks at its select, so this establishes the callers are at
// (or an instant from) the select; it is not a strict parked-at-the-select barrier.
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

func TestSlotTurnoverDoesNotShedExactFitLoad(t *testing.T) {
	// Each iteration hands the held slot to a parked waiter and immediately re-acquires:
	// the vacated queue capacity must be available even while the woken waiter is still
	// being scheduled. (A separate queued-callers counter fails here on most iterations,
	// because the woken waiter keeps counting against the queue until it runs.)
	//
	// The oracle must account for stragglers: a spawned waiter from an earlier iteration
	// can still be live (its slot stolen by main's fast path), legitimately filling the
	// budget. A rejection is judged spurious only when the live spawned callers sampled
	// before the acquire provably left room; otherwise main retries.
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait) // cleanups run LIFO: cancel first, then wait
	t.Cleanup(cancel)

	r, ok := l.Acquire(ctx)
	if !ok {
		t.Fatal("initial acquire")
	}
	var live atomic.Int64 // spawned callers between entry and their release completing
	for i := 0; i < 400; i++ {
		wg.Add(1)
		live.Add(1)
		go func() {
			defer wg.Done()
			defer live.Add(-1)
			for ctx.Err() == nil {
				if r2, ok := l.Acquire(ctx); ok {
					r2()
					return
				}
				runtime.Gosched()
			}
		}()
		waitForWaiting(t, l, 1)
		r() // hand the slot to the waiter
		liveBefore := live.Load()
		var reacquired bool
		r, reacquired = l.Acquire(ctx)
		if !reacquired {
			// Budget is 2 and this caller needs 1: with at most 1 live spawned caller,
			// the budget provably had room, so the shed is spurious.
			if liveBefore <= 1 {
				t.Fatalf("iteration %d: caller shed while the budget had room (live=%d)", i, liveBefore)
			}
			// Stragglers filled the budget; retry once they drain. Healthy code needs at
			// most a handful of rounds, so a bounded deadline turns an accounting
			// regression into a fast failure rather than a package-timeout hang.
			deadline := time.Now().Add(5 * time.Second)
			for !reacquired {
				if time.Now().After(deadline) {
					t.Fatalf("iteration %d: could not re-acquire within 5s; budget eroded?", i)
				}
				runtime.Gosched()
				r, reacquired = l.Acquire(ctx)
			}
		}
	}
	r()
}

func TestCancelledWaitersFreeTheirQueueCapacity(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	r1, _ := l.Acquire(context.Background())
	// Repeatedly park a waiter and cancel it: each cancellation must return its budget
	// reservation, or the queue capacity erodes until admissible callers are shed.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan bool)
		go func() {
			_, ok := l.Acquire(ctx)
			done <- ok
		}()
		waitForWaiting(t, l, 1)
		cancel()
		if ok := <-done; ok {
			t.Fatal("cancelled waiter should have been rejected")
		}
	}
	// Every cancellation must have returned its reservation while the slot is still held
	// (only r1's occupancy unit may remain) AND its parked count -- a leaked parked count
	// inflates Stats.Waiting forever, one per disconnected queued client.
	if got := l.inFlight.Load(); got != 1 {
		t.Fatalf("cancelled waiters leaked occupancy: inFlight=%d, want 1", got)
	}
	if s := l.Stats(); s.Waiting != 0 {
		t.Fatalf("cancelled waiters leaked the parked count: %+v", s)
	}
	// And the queue must still have its full capacity: a fresh waiter can park.
	admitted := make(chan bool)
	go func() {
		r, ok := l.Acquire(context.Background())
		if ok {
			r()
		}
		admitted <- ok
	}()
	waitForWaiting(t, l, 1)
	r1()
	if ok := <-admitted; !ok {
		t.Fatal("queue capacity eroded by cancelled waiters")
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

func TestOverflowRejectionReturnsItsReservation(t *testing.T) {
	// A caller rejected for overflow must back its reservation out. Without the back-out,
	// every overflow rejection permanently erodes the budget by one unit until the limiter
	// rejects everything forever -- the worst accounting failure the design can have.
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	r1, _ := l.Acquire(context.Background())
	admitted := make(chan bool)
	go func() {
		r, ok := l.Acquire(context.Background())
		if ok {
			r()
		}
		admitted <- ok
	}()
	waitForWaiting(t, l, 1) // the queue is now full
	if _, ok := l.Acquire(context.Background()); ok {
		t.Fatal("expected an overflow rejection")
	}
	r1() // drain: the parked waiter is admitted and releases
	if ok := <-admitted; !ok {
		t.Fatal("parked waiter should have been admitted")
	}
	if got := l.inFlight.Load(); got != 0 {
		t.Fatalf("overflow rejection leaked occupancy: inFlight=%d, want 0", got)
	}
	// Full capacity is intact: a holder plus a parked waiter fit again.
	r2, ok := l.Acquire(context.Background())
	if !ok {
		t.Fatal("slot capacity eroded")
	}
	done := make(chan bool)
	go func() {
		r, ok := l.Acquire(context.Background())
		if ok {
			r()
		}
		done <- ok
	}()
	waitForWaiting(t, l, 1)
	r2()
	if ok := <-done; !ok {
		t.Fatal("queue capacity eroded")
	}
}

func TestNegativeMaxQueuedBehavesAsZero(t *testing.T) {
	l := New("t", Params{MaxConcurrent: 2, MaxQueued: -5})
	// Unclamped, a negative queue bound would poison the occupancy limit and reject
	// callers while slots sit free.
	r1, ok1 := l.Acquire(context.Background())
	r2, ok2 := l.Acquire(context.Background())
	if !ok1 || !ok2 {
		t.Fatal("callers within MaxConcurrent must be admitted")
	}
	if _, ok := l.Acquire(context.Background()); ok {
		t.Fatal("expected rejection with no queue capacity")
	}
	if s := l.Stats(); s.MaxQueued != 0 {
		t.Fatalf("negative MaxQueued should normalize to 0: %+v", s)
	}
	r1()
	r2()
}

func TestCloseBeatsARacingRelease(t *testing.T) {
	// A slot released after Close must not be handed to a parked waiter: even when the
	// waiter's slot arm wins the select, it re-checks shutdown, returns the slot, and is
	// rejected. Loop because which select arm wins is random; in most iterations the
	// waiter commits to the shutdown arm before the release lands, so the re-check itself
	// is only reliably exercised by TestSlotWonConcurrentlyWithCloseIsReturned -- that
	// white-box test is the deterministic regression guard for it.
	for i := 0; i < 100; i++ {
		l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
		r, _ := l.Acquire(context.Background())
		got := make(chan bool)
		go func() {
			_, ok := l.Acquire(context.Background())
			got <- ok
		}()
		waitForWaiting(t, l, 1)
		l.Close()
		r() // released after Close: the waiter must not be admitted
		if ok := <-got; ok {
			t.Fatalf("iteration %d: waiter admitted with a slot released after Close", i)
		}
	}
}

func TestSlotWonConcurrentlyWithCloseIsReturned(t *testing.T) {
	// White-box: a waiter that has reserved occupancy and won a slot in the same instant
	// Close lands must, on its re-check, return the slot and be rejected -- this is the
	// specific arm TestCloseBeatsARacingRelease can only reach when scheduling cooperates.
	l := New("t", Params{MaxConcurrent: 1, MaxQueued: 1})
	l.inFlight.Add(1) // the waiter's entry reservation
	<-l.tokens        // the waiter's select wins the slot...
	l.Close()         // ...as shutdown lands
	if _, ok := l.admit(); ok {
		t.Fatal("a slot won concurrently with Close must not be admitted")
	}
	if free := len(l.tokens); free != 1 {
		t.Fatalf("the slot was not returned: %d free", free)
	}
	if got := l.inFlight.Load(); got != 0 {
		t.Fatalf("the rejected admission leaked occupancy: inFlight=%d, want 0", got)
	}
	if s := l.Stats(); s.Held != 0 || s.Waiting != 0 || s.Rejected != 1 {
		t.Fatalf("occupancy not released or rejection not counted: %+v", s)
	}
}

func TestWaitingReportsParkedCallersNotOccupancy(t *testing.T) {
	// Waiting must come from the parked counter, not be derived from occupancy: a
	// reservation mid-admission or mid-rejection is not a waiting caller, and deriving
	// from occupancy is what let Waiting read far beyond the queue bound.
	l := New("t", Params{MaxConcurrent: 2, MaxQueued: 2})
	l.inFlight.Add(1) // occupancy without a parked caller
	if s := l.Stats(); s.Waiting != 0 {
		t.Fatalf("occupancy alone must not report as Waiting: %+v", s)
	}
	l.inFlight.Add(-1)
}

func TestCloseUnblocksWaiters(t *testing.T) {
	// The quiescent case: no slot is released while Close runs (the racing case is
	// TestCloseBeatsARacingRelease).
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
	// The unblocked waiters must have returned their occupancy reservations (only r1's
	// unit may remain) and their parked counts.
	if got := l.inFlight.Load(); got != 1 {
		t.Fatalf("waiters unblocked by Close leaked occupancy: inFlight=%d, want 1", got)
	}
	if s := l.Stats(); s.Waiting != 0 {
		t.Fatalf("waiters unblocked by Close leaked the parked count: %+v", s)
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
