package relayenv

// Verifies the real ld.LDClient's Close() behavior against the SSERelayDataStoreAdapter /
// streamUpdatesStoreWrapper pair, which the fake-client tests could not exercise:
//
//   > streamUpdatesStoreWrapper.Close() closes the underlying store. With handover the retiring
//   > and new clients share one underlying store, so closing the retiring client must NOT close
//   > it — the adapter (not the client) must own the store's lifecycle. (Not reproducible with
//   > the fake client; verified here against the real client.)

import (
	"testing"
	"time"

	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/store"

	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
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
func (c *closeObservingStore) IsInitialized() bool { return c.inner.IsInitialized() }
func (c *closeObservingStore) IsStatusMonitoringEnabled() bool {
	return c.inner.IsStatusMonitoringEnabled()
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
