package store

// Refcount contract tests for the store-handover wrapper (re-anchor).
//
// These cover the two properties the refcount design hinges on but the original suite left
// unexercised (multi-agent review, PR #736): Close idempotency past the final release, and safety of a
// Build-reuse (acquire) racing the retiring client's Close. Run the package with -race.

import (
	"sync"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingCloseStore counts how many times Close is invoked on the underlying store, so a double
// teardown is observable. Count is mutex-guarded so the concurrency test can read it safely.
type countingCloseStore struct {
	subsystems.DataStore
	mu    sync.Mutex
	count int
}

func (s *countingCloseStore) Close() error {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	return s.DataStore.Close()
}

func (s *countingCloseStore) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

type countingCloseStoreFactory struct {
	mu    sync.Mutex
	built []*countingCloseStore
}

func (f *countingCloseStoreFactory) Build(_ subsystems.ClientContext) (subsystems.DataStore, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &countingCloseStore{DataStore: sharedtest.NewInMemoryStore()}
	f.built = append(f.built, s)
	return s, nil
}

func (f *countingCloseStoreFactory) allBuilt() []*countingCloseStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*countingCloseStore(nil), f.built...)
}

// currentRefCount reads the wrapper's holder count under refMu, so tests can inspect it while other
// goroutines may be calling acquire/Close. Test-only helper (kept out of the production file so the
// unused linter doesn't flag it).
func (sw *streamUpdatesStoreWrapper) currentRefCount() int {
	sw.refMu.Lock()
	defer sw.refMu.Unlock()
	return sw.refCount
}

// TestWrapperCloseIsIdempotent: once the final holder has released the wrapper and torn down the
// underlying store, a stray extra Close must be a no-op — not decrement refCount below zero and
// re-close the underlying store (which double-releases a persistent store's connection pool).
func TestWrapperCloseIsIdempotent(t *testing.T) {
	factory := &countingCloseStoreFactory{}
	adapter := NewSSERelayDataStoreAdapter(factory, &mockEnvStreamsUpdates{})

	sw, err := adapter.Build(subsystems.BasicClientContext{})
	require.NoError(t, err)

	require.NoError(t, sw.Close())
	require.Equal(t, 1, factory.built[0].closeCount(), "final Close tears down the underlying store once")

	require.NoError(t, sw.Close())
	assert.Equal(t, 1, factory.built[0].closeCount(),
		"Close is idempotent: a second Close must not re-close the underlying store")
}

// TestWrapperFullyClosedIsNotResurrected: once the final holder has released the wrapper and torn down
// the underlying store, acquire must refuse it, so a later Build (e.g. a subsequent re-anchor) rebuilds a
// fresh wrapper over a fresh, open store rather than handing back the closed one — a use-after-close for
// a persistent store whose Close releases its connection pool.
//
// (The re-anchor flow keeps the anchor client permanent, so refCount doesn't reach zero while the env is
// alive today; this guards the wrapper/adapter contract itself against a future caller.)
func TestWrapperFullyClosedIsNotResurrected(t *testing.T) {
	factory := &countingCloseStoreFactory{}
	adapter := NewSSERelayDataStoreAdapter(factory, &mockEnvStreamsUpdates{})

	first, err := adapter.Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	sw1 := first.(*streamUpdatesStoreWrapper)
	require.Equal(t, 1, sw1.currentRefCount())

	// The sole client shuts down: refCount 1 -> 0, wrapper marked closed, underlying store torn down.
	require.NoError(t, first.Close())
	require.Equal(t, 1, factory.built[0].closeCount())
	assert.False(t, sw1.acquire(), "acquire on a fully-closed wrapper must return false")

	second, err := adapter.Build(subsystems.BasicClientContext{})
	require.NoError(t, err)
	sw2 := second.(*streamUpdatesStoreWrapper)

	assert.NotSame(t, sw1, sw2, "adapter must rebuild rather than hand back the torn-down wrapper")
	assert.Equal(t, 1, sw2.currentRefCount(), "the fresh wrapper starts at refCount 1")
	assert.Same(t, sw2, adapter.GetStore(), "the adapter now points at the fresh wrapper")
	require.Len(t, factory.allBuilt(), 2, "a fresh underlying store was built for the fresh wrapper")
	assert.Equal(t, 0, factory.built[1].closeCount(), "the fresh wrapper's underlying store is open")
}

// TestWrapperConcurrentCloseClosesExactlyOnce fires many concurrent Close calls on a single wrapper
// (modelling stray/duplicate client Closes arriving at once) and asserts the underlying store is torn
// down exactly once. This directly exercises the idempotent early-return branch under -race and is
// non-vacuous: against a non-idempotent Close, concurrent duplicates drive refCount past zero and
// re-close the underlying store (closeCount > 1).
func TestWrapperConcurrentCloseClosesExactlyOnce(t *testing.T) {
	const iterations = 300
	for i := 0; i < iterations; i++ {
		factory := &countingCloseStoreFactory{}
		adapter := NewSSERelayDataStoreAdapter(factory, &mockEnvStreamsUpdates{})

		sw, err := adapter.Build(subsystems.BasicClientContext{})
		require.NoError(t, err)

		const closers = 8
		var wg sync.WaitGroup
		wg.Add(closers)
		for c := 0; c < closers; c++ {
			go func() { defer wg.Done(); assert.NoError(t, sw.Close()) }()
		}
		wg.Wait()

		require.Equal(t, 1, factory.built[0].closeCount(),
			"the underlying store must be closed exactly once regardless of duplicate concurrent Closes")
	}
}

// TestWrapperHandoverCloseRace: a re-anchor's second Build (reuse -> acquire, or a fresh rebuild if the
// wrapper is already closed) racing the retiring client's Close must never close a single underlying
// store more than once, and -race must stay clean.
func TestWrapperHandoverCloseRace(t *testing.T) {
	const iterations = 500
	for i := 0; i < iterations; i++ {
		factory := &countingCloseStoreFactory{}
		adapter := NewSSERelayDataStoreAdapter(factory, &mockEnvStreamsUpdates{})

		first, err := adapter.Build(subsystems.BasicClientContext{})
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(2)
		// A re-anchor stands up a second client: Build reuses the live wrapper (acquire) or, if the
		// retiring Close already tore it down, rebuilds a fresh one.
		go func() {
			defer wg.Done()
			second, buildErr := adapter.Build(subsystems.BasicClientContext{})
			assert.NoError(t, buildErr)
			_ = second
		}()
		// The retiring client closes concurrently.
		go func() {
			defer wg.Done()
			_ = first.Close()
		}()
		wg.Wait()

		for _, s := range factory.allBuilt() {
			require.LessOrEqual(t, s.closeCount(), 1, "an underlying store was closed more than once")
		}
	}
}
