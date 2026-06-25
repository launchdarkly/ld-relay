package relayenv

// Spike for SDK-2542 (T2.c) — verifies the real ld.LDClient's Close() behavior against the
// SSERelayDataStoreAdapter / streamUpdatesStoreWrapper pair, which the T0 PoC could not exercise (it
// used a fake client). The design (phase1-design.md §7) flags this as the single remaining
// PoC-unvalidated piece:
//
//   > streamUpdatesStoreWrapper.Close() closes the underlying store. With handover the retiring
//   > and new clients share one underlying store, so closing the retiring client must NOT close
//   > it — the adapter (not the client) must own the store's lifecycle. (Not reproducible with
//   > the fake client used in the PoC; verify against the real client in T2.c.)
//
// We answer two questions:
//   Q1. Does ld.LDClient.Close() invoke Close() on its data store (the wrapper)?
//   Q2. After the wrapper's Close() runs, is the underlying store still usable for reads?
//
// Q1 determines whether store handover is at risk at all. Q2 determines whether the remedy needs to
// gate the wrapper's Close (case A: in-memory Close is destructive) or whether it can stay as-is
// (case B: in-memory Close is a no-op and reads still work).

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/store"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closeObservingStore wraps an in-memory data store factory so we can directly observe when the
// underlying store's Close() is invoked. This is the unambiguous signal that ld.LDClient.Close()
// propagates through streamUpdatesStoreWrapper.Close() to the wrapped store.
type closeObservingStore struct {
	inner      subsystems.DataStore
	closeCount int
}

func (c *closeObservingStore) Close() error {
	c.closeCount++
	return c.inner.Close()
}
func (c *closeObservingStore) Init(d []ldstoretypes.Collection) error { return c.inner.Init(d) }
func (c *closeObservingStore) Get(k ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	return c.inner.Get(k, key)
}
func (c *closeObservingStore) GetAll(k ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	return c.inner.GetAll(k)
}
func (c *closeObservingStore) Upsert(k ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) (bool, error) {
	return c.inner.Upsert(k, key, item)
}
func (c *closeObservingStore) IsInitialized() bool             { return c.inner.IsInitialized() }
func (c *closeObservingStore) IsStatusMonitoringEnabled() bool { return c.inner.IsStatusMonitoringEnabled() }

type closeObservingStoreFactory struct {
	observed *closeObservingStore
}

func (f *closeObservingStoreFactory) Build(ctx subsystems.ClientContext) (subsystems.DataStore, error) {
	inner, err := ldcomponents.InMemoryDataStore().Build(ctx)
	if err != nil {
		return nil, err
	}
	f.observed = &closeObservingStore{inner: inner}
	return f.observed, nil
}

// realClientUsingAdapter spins up a real ld.LDClient backed by the relay store adapter. Using
// ExternalUpdatesOnly as the data source avoids any network calls (no upstream streaming connection
// is opened), so the test is hermetic. The adapter sees the real client's DataStore.Build() call
// and the real Close() lifecycle on shutdown.
func realClientUsingAdapter(t *testing.T, adapter *store.SSERelayDataStoreAdapter) *ld.LDClient {
	t.Helper()
	cfg := ld.Config{
		DataStore:  adapter,
		DataSource: ldcomponents.ExternalUpdatesOnly(),
		Events:     ldcomponents.NoEvents(),
	}
	client, err := ld.MakeCustomClient("fake-sdk-key", cfg, 5*time.Second)
	require.NoError(t, err)
	return client
}

// TestRealClient_CloseInvokesWrapperClose verifies that closing a real ld.LDClient causes the
// wrapped store's Close() to fire. This is the precondition that makes store handover dangerous —
// if Close did not propagate, there would be no lifecycle hazard to design around.
func TestRealClient_CloseInvokesWrapperClose(t *testing.T) {
	factory := &closeObservingStoreFactory{}
	rec := &recordingStreamUpdates{}
	adapter := store.NewSSERelayDataStoreAdapter(factory, rec)

	client := realClientUsingAdapter(t, adapter)
	require.NotNil(t, factory.observed, "the adapter must have built the observed store")
	require.Equal(t, 0, factory.observed.closeCount, "no Close yet before client.Close")

	wrapper := adapter.GetStore()
	require.NoError(t, wrapper.Init(st.AllData))
	require.True(t, wrapper.IsInitialized())

	require.NoError(t, client.Close())

	// The headline finding: closing the real client propagates Close() to the underlying store via
	// streamUpdatesStoreWrapper.Close(). If this assertion fails, the design's lifecycle caveat is
	// not a real hazard for this combination and T2.c's store-handover fix only needs the Build()
	// reuse, not a Close() lifecycle change.
	assert.Equal(t, 1, factory.observed.closeCount,
		"ld.LDClient.Close should propagate to the underlying data store via the wrapper")
}

// TestRealClient_ReadsAfterCloseAreStillFunctional asks the second question: after Close runs, is
// the underlying in-memory store still usable for Get? The answer tells us whether the fix in T2.c
// needs to actually prevent Close (because reads will fail after it) or whether reads coincidentally
// still work (because the in-memory store's Close is effectively a no-op for read behavior). Even
// if reads happen to work, T2.c should still gate Close — relying on undocumented "Close is a
// no-op" behavior is brittle and breaks when persistent stores enter the picture.
func TestRealClient_ReadsAfterCloseAreStillFunctional(t *testing.T) {
	factory := &closeObservingStoreFactory{}
	rec := &recordingStreamUpdates{}
	adapter := store.NewSSERelayDataStoreAdapter(factory, rec)

	client := realClientUsingAdapter(t, adapter)
	wrapper := adapter.GetStore()
	require.NoError(t, wrapper.Init(st.AllData))

	featureKind := ldstoreimpl.Features()
	flagKey := st.Flag1ServerSide.Flag.Key

	got, err := wrapper.Get(featureKind, flagKey)
	require.NoError(t, err)
	require.NotNil(t, got.Item, "sanity: data is readable before close")

	require.NoError(t, client.Close())

	// Read after Close. The outcome here is informational, not a pass/fail design gate:
	//   - If the read succeeds, the in-memory store's Close is effectively a no-op for queries; T2.c
	//     can still safely gate Close to be defensive (persistent stores may differ).
	//   - If the read fails, T2.c MUST gate Close, since the new anchor would observe a broken store.
	gotAfter, errAfter := wrapper.Get(featureKind, flagKey)
	t.Logf("Get after client.Close: item=%v err=%v initialized=%v",
		gotAfter.Item != nil, errAfter, wrapper.IsInitialized())
}

// TestRealClient_HandoverPreservesUnderlyingStore exercises the production store-handover behavior:
// when a second real ld.LDClient is built against the same SSERelayDataStoreAdapter — the re-anchor
// case — the adapter hands the existing wrapper (and underlying store) to the new client rather
// than rebuilding it. Closing the first client must not tear the underlying store down while the
// second client is still holding it; only the final Close releases it.
//
// Sequence:
//  1. Build client1; init data on its wrapper.
//  2. Build client2 — adapter returns the SAME wrapper; no second underlying store is built.
//  3. Close client1 — store stays open because client2 still holds it.
//  4. Close client2 — final release; underlying store closes exactly once.
func TestRealClient_HandoverPreservesUnderlyingStore(t *testing.T) {
	factory := &countingStoreFactory{}
	rec := &recordingStreamUpdates{}
	adapter := store.NewSSERelayDataStoreAdapter(factory, rec)

	client1 := realClientUsingAdapter(t, adapter)
	wrapper1 := adapter.GetStore()
	require.NoError(t, wrapper1.Init(st.AllData))
	require.Equal(t, 1, factory.buildCount, "one Build for client1")
	underlying := factory.lastObserved

	client2 := realClientUsingAdapter(t, adapter)
	wrapper2 := adapter.GetStore()
	require.Equal(t, 1, factory.buildCount, "client2 reuses the existing wrapper — no second Build")
	assert.Same(t, wrapper1, wrapper2, "the adapter hands the same wrapper to both clients")
	assert.True(t, wrapper2.IsInitialized(), "the shared store stays initialized across handover")

	require.NoError(t, client1.Close())
	assert.Equal(t, 0, underlying.closeCount,
		"client1.Close must NOT tear down the underlying store while client2 still holds it")

	require.NoError(t, client2.Close())
	assert.Equal(t, 1, underlying.closeCount,
		"client2.Close is the final release; the underlying store is closed exactly once")
}

// countingStoreFactory tracks every Build call and exposes the most recently built store so the
// handover test can compare instances across builds. Each build wraps a real in-memory store in a
// closeObservingStore so close events are visible.
type countingStoreFactory struct {
	buildCount   int
	lastObserved *closeObservingStore
}

func (f *countingStoreFactory) Build(ctx subsystems.ClientContext) (subsystems.DataStore, error) {
	inner, err := ldcomponents.InMemoryDataStore().Build(ctx)
	if err != nil {
		return nil, err
	}
	observed := &closeObservingStore{inner: inner}
	f.buildCount++
	f.lastObserved = observed
	return observed, nil
}
