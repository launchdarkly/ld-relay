package store

// Regression test for a race in SSERelayDataStoreAdapter.Build: two Build calls that both observe
// no existing store — the environment's initial anchor-client build racing the first re-anchor's
// synchronous build — must not each construct and install their own wrapper. If they did, the last
// writer would win adapter.store, and when that writer's client is later discarded as superseded, its
// Close would tear down the store the adapter is serving — evaluations and stream queries would read
// a closed store while the live upstream client fed one nothing reads. Build holds the adapter lock
// across the whole build, so the two calls are serialized: the first installs its wrapper and the
// second adopts that same wrapper via the fast path rather than building its own.
//
// The realistic trigger is a persistent store whose construction is slow at startup (e.g. Redis
// briefly unreachable) while a rotation patch re-anchors the environment.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatedStoreFactory stalls its first Build call inside the wrapped factory until released,
// modelling a slow persistent-store connection at startup. Later calls proceed immediately.
type gatedStoreFactory struct {
	inner   countingCloseStoreFactory
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (f *gatedStoreFactory) Build(ctx subsystems.ClientContext) (subsystems.DataStore, error) {
	if f.calls.Add(1) == 1 {
		f.entered <- struct{}{}
		<-f.release
	}
	return f.inner.Build(ctx)
}

func TestConcurrentBuildSerializesAndSharesOneWrapper(t *testing.T) {
	factory := &gatedStoreFactory{entered: make(chan struct{}, 1), release: make(chan struct{})}
	adapter := NewSSERelayDataStoreAdapter(factory, &mockEnvStreamsUpdates{})

	// The initial anchor client's build stalls inside the wrapped factory. Because Build holds the
	// adapter lock across the whole build, it holds the lock for the duration of this stall.
	firstResult := make(chan subsystems.DataStore, 1)
	go func() {
		sw, err := adapter.Build(subsystems.BasicClientContext{})
		assert.NoError(t, err)
		firstResult <- sw
	}()
	<-factory.entered

	// A re-anchor's synchronous build starts while the first is stalled. It must block on the adapter
	// lock — it cannot build and install its own wrapper.
	secondResult := make(chan subsystems.DataStore, 1)
	go func() {
		sw, err := adapter.Build(subsystems.BasicClientContext{})
		assert.NoError(t, err)
		secondResult <- sw
	}()

	select {
	case <-secondResult:
		t.Fatal("the second build returned while the first still held the lock; builds were not serialized")
	case <-time.After(100 * time.Millisecond):
	}

	// Release the stalled build. It installs its wrapper; the second build then adopts that same
	// wrapper via the fast path rather than building its own.
	close(factory.release)
	firstStore := <-firstResult
	secondStore := <-secondResult

	require.Same(t, firstStore, secondStore, "both builds must share the one installed wrapper")
	require.Same(t, firstStore, adapter.GetStore())
	require.Equal(t, int32(1), factory.calls.Load(), "the wrapped factory must be built exactly once")

	built := factory.inner.allBuilt()
	require.Len(t, built, 1, "no discarded second wrapper was ever constructed")

	// The first build's client is later discarded as superseded and closed. That releases one handover
	// reference; the adapter keeps serving an open store.
	require.NoError(t, firstStore.Close())
	require.Same(t, secondStore, adapter.GetStore())
	sw, ok := adapter.GetStore().(*streamUpdatesStoreWrapper)
	require.True(t, ok)
	sw.refMu.Lock()
	closed := sw.closed
	sw.refMu.Unlock()
	require.False(t, closed, "the adapter must not be serving a torn-down store")
	assert.Equal(t, 0, built[0].closeCount(), "the underlying store remains open while a holder remains")

	// The final holder's release (environment teardown) closes the underlying store exactly once.
	require.NoError(t, secondStore.Close())
	assert.Equal(t, 1, built[0].closeCount())
}
