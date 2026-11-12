package filedata

import (
	"fmt"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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

// offlineTestData returns archive data with flagCount flags, so that each load in a test can be
// told apart by its number of flags.
func offlineTestData(flagCount int) []ldstoretypes.Collection {
	items := make([]ldstoretypes.KeyedItemDescriptor, 0, flagCount)
	for i := 0; i < flagCount; i++ {
		flag := ldbuilders.NewFlagBuilder(fmt.Sprintf("flag%d", i)).Version(1).Build()
		items = append(items, ldstoretypes.KeyedItemDescriptor{
			Key:  flag.Key,
			Item: ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag},
		})
	}
	return []ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: items}}
}

func flagCount(r subsystems.DataSynchronizerResult) int {
	return len(r.ChangeSet.Changes())
}

func TestOfflineModeSynchronizerSendsFullTransferWithNoSelector(t *testing.T) {
	requireFullTransferWithNoSelector := func(r subsystems.DataSynchronizerResult) {
		t.Helper()
		if code := r.ChangeSet.IntentCode(); code != subsystems.IntentTransferFull {
			t.Fatalf("changeSet intent was %q; want %q", code, subsystems.IntentTransferFull)
		}
		if sel := r.ChangeSet.Selector(); sel.IsDefined() {
			t.Fatalf("changeSet had selector %q/%d; want none", sel.State(), sel.Version())
		}
	}

	d := NewOfflineModeData(offlineTestData(1))
	s := buildOfflineModeSynchronizer(t, d)
	defer s.Close()
	ch := s.Sync(nil)
	requireFullTransferWithNoSelector(receiveResult(t, ch))

	if err := d.UpdateData(offlineTestData(2)); err != nil {
		t.Fatal(err)
	}
	requireFullTransferWithNoSelector(receiveResult(t, ch))
}

func TestOfflineModeSynchronizerSlowConsumerEndsOnNewestData(t *testing.T) {
	d := NewOfflineModeData(offlineTestData(0))
	s := buildOfflineModeSynchronizer(t, d)
	defer s.Close()
	ch := s.Sync(nil)

	first := receiveResult(t, ch)
	if first.ChangeSet == nil {
		t.Fatal("initial result had no changeSet")
	}

	// The consumer stalls across many archive reloads. It must still end on the newest data,
	// because no later update will arrive to correct a stale one.
	const updates = 20
	for i := 1; i <= updates; i++ {
		if err := d.UpdateData(offlineTestData(i)); err != nil {
			t.Fatal(err)
		}
	}

	last := -1
	for last != updates {
		select {
		case r := <-ch:
			last = flagCount(r)
		case <-time.After(time.Second):
			t.Fatalf("consumer ended on data with %d flags; want %d", last, updates)
		}
	}
}

func TestOfflineModeSynchronizerRebuiltAfterCloseReadsCurrentData(t *testing.T) {
	d := NewOfflineModeData(offlineTestData(1))

	first := buildOfflineModeSynchronizer(t, d)
	firstCh := first.Sync(nil)
	if n := flagCount(receiveResult(t, firstCh)); n != 1 {
		t.Fatalf("first synchronizer started on data with %d flags; want 1", n)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, firstCh)

	// The archive changes while no synchronizer runs.
	if err := d.UpdateData(offlineTestData(2)); err != nil {
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
	if n := flagCount(r); n != 2 {
		t.Fatalf("rebuilt synchronizer started on data with %d flags; want 2", n)
	}

	// The new synchronizer stays open and receives later updates.
	if err := d.UpdateData(offlineTestData(3)); err != nil {
		t.Fatal(err)
	}
	if n := flagCount(receiveResult(t, secondCh)); n != 3 {
		t.Fatalf("rebuilt synchronizer received data with %d flags; want 3", n)
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
