package filedata

import (
	"testing"
	"time"
)

func TestOfflineModeSynchronizerSlowConsumerEndsOnNewestVersion(t *testing.T) {
	s := NewOfflineModeSynchronizer(nil)
	defer s.Close()
	ch := s.Sync(nil)

	first := <-ch
	if first.ChangeSet == nil {
		t.Fatal("initial result had no changeSet")
	}

	// The consumer stalls across many archive reloads. It must still end on the newest version,
	// because no later update will arrive to correct a stale one.
	const updates = 20
	for i := 0; i < updates; i++ {
		if err := s.UpdateData(nil); err != nil {
			t.Fatal(err)
		}
	}

	want := 1 + updates
	var last int
	for last != want {
		select {
		case r := <-ch:
			last = r.ChangeSet.Selector().Version()
		case <-time.After(time.Second):
			t.Fatalf("consumer ended on version %d; want %d", last, want)
		}
	}
}
