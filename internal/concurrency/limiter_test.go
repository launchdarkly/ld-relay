package concurrency

import (
	"context"
	"sync"
	"testing"
	"time"
)

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
	// Give the goroutine time to enter the backlog.
	time.Sleep(50 * time.Millisecond)
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
	defer r1()
	go l.Acquire(context.Background()) // fills the single backlog slot
	time.Sleep(50 * time.Millisecond)
	if _, ok := l.Acquire(context.Background()); ok {
		t.Fatal("expected rejection when backlog is full")
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
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("expected rejection on context cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not unblocked by context cancel")
	}
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
