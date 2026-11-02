package filedata

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
)

func buildOfflineModeSynchronizer(t *testing.T, d *OfflineModeData) subsystems.DataSynchronizer {
	t.Helper()
	s, err := OfflineModeSynchronizerFactory{Data: d}.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func receiveResult(t *testing.T, ch <-chan subsystems.DataSynchronizerResult) subsystems.DataSynchronizerResult {
	t.Helper()
	select {
	case r, ok := <-ch:
		if !ok {
			t.Fatal("result channel closed unexpectedly")
		}
		return r
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a result")
	}
	return subsystems.DataSynchronizerResult{}
}

func requireClosed(t *testing.T, ch <-chan subsystems.DataSynchronizerResult) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("result channel did not close")
		}
	}
}

func TestOfflineModeSynchronizerSlowConsumerEndsOnNewestVersion(t *testing.T) {
	d := NewOfflineModeData(nil)
	s := buildOfflineModeSynchronizer(t, d)
	defer s.Close()
	ch := s.Sync(nil)

	first := receiveResult(t, ch)
	if first.ChangeSet == nil {
		t.Fatal("initial result had no changeSet")
	}

	// The consumer stalls across many archive reloads. It must still end on the newest version,
	// because no later update will arrive to correct a stale one.
	const updates = 20
	for i := 0; i < updates; i++ {
		if err := d.UpdateData(nil); err != nil {
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

func TestOfflineModeSynchronizerRebuiltAfterCloseReadsCurrentData(t *testing.T) {
	d := NewOfflineModeData(nil)

	first := buildOfflineModeSynchronizer(t, d)
	firstCh := first.Sync(nil)
	if v := receiveResult(t, firstCh).ChangeSet.Selector().Version(); v != 1 {
		t.Fatalf("first synchronizer started on version %d; want 1", v)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, firstCh)

	// The archive changes while no synchronizer runs.
	if err := d.UpdateData(nil); err != nil {
		t.Fatal(err)
	}

	// The SDK builds the synchronizer again. The new synchronizer starts on the current data.
	second := buildOfflineModeSynchronizer(t, d)
	defer second.Close()
	secondCh := second.Sync(nil)
	r := receiveResult(t, secondCh)
	if r.State != interfaces.DataSourceStateValid {
		t.Fatalf("rebuilt synchronizer reported state %s; want VALID", r.State)
	}
	if v := r.ChangeSet.Selector().Version(); v != 2 {
		t.Fatalf("rebuilt synchronizer started on version %d; want 2", v)
	}

	// The new synchronizer stays open and receives later updates.
	if err := d.UpdateData(nil); err != nil {
		t.Fatal(err)
	}
	if v := receiveResult(t, secondCh).ChangeSet.Selector().Version(); v != 3 {
		t.Fatalf("rebuilt synchronizer received version %d; want 3", v)
	}
}

func TestOfflineModeSynchronizerCloseEndsSyncWhenNothingReads(t *testing.T) {
	d := NewOfflineModeData(nil)
	s := buildOfflineModeSynchronizer(t, d)
	ch := s.Sync(nil)

	// Nothing reads the initial result, so the Sync goroutine waits to send it. Close must end the
	// goroutine and close the channel.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, ch)
}
