package store

// Regression test for a race in SSERelayDataStoreAdapter.Build: two Build calls that both observe
// no existing store — the environment's initial anchor-client build racing the first re-anchor's
// synchronous build — each construct their own wrapper. Build must re-check under the lock before
// installing, and the build that finishes last must adopt the already-installed wrapper (a normal
// handover) instead of overwriting it. Without the re-check, the last writer wins adapter.store,
// and when that writer's client is then discarded as superseded, its Close tears down the store the
// adapter is serving — evaluations and stream queries read a closed store while the live upstream
// client feeds one nothing reads.
//
// The realistic trigger is a persistent store whose construction is slow at startup (e.g. Redis
// briefly unreachable) while a rotation patch re-anchors the environment.

import (
	"sync/atomic"
	"testing"

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

func TestConcurrentBuildAdoptsInstalledWrapperInsteadOfOverwriting(t *testing.T) {
	factory := &gatedStoreFactory{entered: make(chan struct{}, 1), release: make(chan struct{})}
	adapter := NewSSERelayDataStoreAdapter(factory, &mockEnvStreamsUpdates{})

	// The initial anchor client's build: passes Build's fast-path nil check, then stalls inside
	// the wrapped factory before any store assignment.
	firstResult := make(chan subsystems.DataStore, 1)
	go func() {
		sw, err := adapter.Build(subsystems.BasicClientContext{})
		assert.NoError(t, err)
		firstResult <- sw
	}()
	<-factory.entered

	// A re-anchor's synchronous build runs while the first build is stalled: it also sees no
	// existing store, builds its own wrapper, and installs it. Its client goes on to initialize
	// and become the committed anchor, so this is the wrapper the environment serves from.
	secondStore, err := adapter.Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	require.Same(t, secondStore, adapter.GetStore(),
		"sanity: the second build's wrapper is installed while the first is still stalled")

	// The stalled build completes. It must adopt the installed wrapper (handover) rather than
	// overwrite it with its own.
	close(factory.release)
	firstStore := <-firstResult
	require.Same(t, secondStore, firstStore,
		"the late build must return the already-installed wrapper, not its own")

	// The late build's own underlying store was never exposed and must have been closed; the
	// installed wrapper's underlying store stays open. (The factory builds in completion order:
	// index 0 is the second/installed build, index 1 is the late/discarded one.)
	built := factory.inner.allBuilt()
	require.Len(t, built, 2)
	assert.Equal(t, 0, built[0].closeCount(), "the surviving underlying store remains open")
	assert.Equal(t, 1, built[1].closeCount(), "the discarded build's underlying store is closed once")

	// The first build's client is later discarded as superseded and closed. That releases its
	// handover reference; the adapter keeps serving an open store.
	require.NoError(t, firstStore.Close())
	current := adapter.GetStore()
	require.Same(t, secondStore, current)
	sw, ok := current.(*streamUpdatesStoreWrapper)
	require.True(t, ok)
	sw.refMu.Lock()
	closed := sw.closed
	sw.refMu.Unlock()
	require.False(t, closed, "the adapter must not be serving a torn-down store")
	assert.Equal(t, 0, built[0].closeCount())

	// The final holder's release (environment teardown) closes the underlying store exactly once.
	require.NoError(t, secondStore.Close())
	assert.Equal(t, 1, built[0].closeCount())
}
