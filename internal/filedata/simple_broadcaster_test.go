package filedata

import (
	"testing"
	"time"
)

// The broadcaster's newest-value guarantee is what keeps an offline-mode SDK correct when it
// cannot keep up with archive reloads: every changeSet is a full transfer, so a
// listener that loses intermediate updates only loses latency, never a version -- as long as
// the newest broadcast always survives. These tests pin exactly that.

func TestSimpleBroadcasterNewestValueSurvivesSlowListener(t *testing.T) {
	b := newSimpleBroadcaster[int]()
	chA := b.AddListener()
	chB := b.AddListener()
	defer b.RemoveListener(chA)
	defer b.RemoveListener(chB)

	// Broadcast 45 values while neither listener reads. The mailbox holds one slot, so each
	// listener must wake holding 45, the last value: an intermediate one would strand the
	// listener on stale state forever if no further updates ever came.
	for i := 1; i <= 45; i++ {
		b.Broadcast(i)
	}

	for _, ch := range []<-chan int{chA, chB} {
		select {
		case got := <-ch:
			if got != 45 {
				t.Fatalf("slow listener was offered update %d; want the newest value 45", got)
			}
		case <-time.After(time.Second):
			t.Fatal("slow listener received nothing")
		}
		select {
		case got := <-ch:
			t.Fatalf("slow listener had a second value %d queued; the mailbox keeps only the newest", got)
		default:
		}
	}
}

func TestSimpleBroadcasterListenerDoesNotBlockBroadcaster(t *testing.T) {
	b := newSimpleBroadcaster[int]()
	ch := b.AddListener()
	defer b.RemoveListener(ch)

	// A listener that never reads must not slow the broadcast path, and later broadcasts must
	// still make it past the stalled listener -- being visible when it resumes.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			b.Broadcast(i)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("1000 broadcasts to a non-reading listener did not finish; Broadcast blocked")
	}

	select {
	case got := <-ch:
		if got != 999 {
			t.Fatalf("listener late to the party got %d; want 999", got)
		}
	default:
		t.Fatal("listener received nothing after broadcasts")
	}
}

func TestSimpleBroadcasterKeepsIndependentListeners(t *testing.T) {
	b := newSimpleBroadcaster[int]()
	chA := b.AddListener()
	chB := b.AddListener()
	defer b.RemoveListener(chA)
	defer b.RemoveListener(chB)

	b.Broadcast(1)
	// A reads but does not drain in any special order; B has not read at all.
	if got := <-chA; got != 1 {
		t.Fatalf("first listener lost its value: got %d", got)
	}
	b.Broadcast(2)

	// A finds only the freshest one even though it is mid-stream, and B, coming late to the
	// same broadcasts, finds the same newest value.
	if got := <-chA; got != 2 {
		t.Fatalf("A got %d after reading 1; want 2", got)
	}
	if got := <-chB; got != 2 {
		t.Fatalf("B got %d; want 2, the newest broadcast", got)
	}
	select {
	case got := <-chB:
		t.Fatalf("B had a leftover value %d; nothing should queue behind the newest", got)
	default:
	}
}

func TestSimpleBroadcasterRemoveListenerClosesAndDetaches(t *testing.T) {
	b := newSimpleBroadcaster[int]()
	ch := b.AddListener()
	b.Broadcast(1)
	b.RemoveListener(ch)

	// The channel closes on removal; late broadcasts must not panic on the detached channel.
	b.Broadcast(2)

	// The removed listener closes. Its buffered value, broadcast before the removal, is still
	// readable -- closing does not drain -- and after it the channel reports closed forever.
	if _, ok := <-ch; !ok {
		t.Fatal("expected the pre-removal buffered value to still be readable")
	}
	if got, ok := <-ch; ok {
		t.Fatalf("removed listener delivered %d after closing", got)
	}
}
