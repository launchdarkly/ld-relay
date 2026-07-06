package store

// Regression test for the store-handover refcount contract (concurrent-keys re-anchor).
//
// SSERelayDataStoreAdapter.Build reuses whatever wrapper is parked in a.store so a re-anchor can hand
// the populated store to the new client. The hazard: once the wrapper's refCount reaches zero and its
// underlying store is torn down, a later Build must NOT hand that same, now-closed wrapper back to a
// new client (a use-after-close for a persistent store whose Close releases its connection pool). The
// fix marks a fully-closed wrapper and has acquire refuse it, so Build rebuilds a fresh wrapper.
//
// (The re-anchor flow keeps the anchor client permanent, so refCount doesn't reach zero while the env
// is alive today; this guards the wrapper/adapter contract itself against a future caller.)

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshStoreFactory builds a new underlying store on every Build, mirroring a real DataStore factory
// (mockStoreFactory returns a single fixed instance, which can't model a rebuild).
type freshStoreFactory struct {
	built []*mockStore
}

func (f *freshStoreFactory) Build(_ subsystems.ClientContext) (subsystems.DataStore, error) {
	s := &mockStore{realStore: sharedtest.NewInMemoryStore()}
	f.built = append(f.built, s)
	return s, nil
}

func TestStoreAdapterRebuildsAfterFullClose(t *testing.T) {
	factory := &freshStoreFactory{}
	updates := &mockEnvStreamsUpdates{}
	adapter := NewSSERelayDataStoreAdapter(factory, updates)
	ctx := subsystems.BasicClientContext{}

	// First client builds the wrapper: refCount = 1.
	first, err := adapter.Build(ctx)
	require.NoError(t, err)
	sw1 := first.(*streamUpdatesStoreWrapper)
	require.Equal(t, 1, sw1.currentRefCount())

	// The sole client shuts down: refCount 1 -> 0, wrapper marked closed, underlying store torn down.
	require.NoError(t, first.Close())
	require.True(t, factory.built[0].closed, "final Close tears down the underlying store")

	// A subsequent Build (e.g. a later re-anchor) must NOT resurrect the fully-closed wrapper — it
	// rebuilds a fresh wrapper backed by a fresh, open store.
	second, err := adapter.Build(ctx)
	require.NoError(t, err)
	sw2 := second.(*streamUpdatesStoreWrapper)

	assert.NotSame(t, sw1, sw2, "adapter must rebuild rather than hand back the torn-down wrapper")
	assert.Equal(t, 1, sw2.currentRefCount(), "the fresh wrapper starts at refCount 1")
	assert.False(t, sw2.store.(*mockStore).closed, "the fresh wrapper's underlying store is open")
	assert.Same(t, sw2, adapter.GetStore(), "the adapter now points at the fresh wrapper")

	// acquire on the fully-closed wrapper refuses, so it can never be resurrected.
	assert.False(t, sw1.acquire(), "acquire on a fully-closed wrapper must return false")
}
