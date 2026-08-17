package relayenv

// Tests that the big-segment synchronizer follows the anchor across a re-anchor: recreated on the
// new anchor key, an already-started sync continues while the old one closes, and a rolled-back
// re-anchor does not rewire.
//
// The unconfigured case (bigSegmentSync nil) needs no test of its own: makeBasicEnv leaves the field
// nil, so every re-anchor test built on it would panic in reanchorBigSegmentSync's old.Close() if the
// nil guard were removed.

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBigSegmentTestEnv(
	t *testing.T,
	storeFactory bigsegments.BigSegmentStoreFactory,
	clientFactory sdks.ClientFactoryFunc,
	capturing *capturingBigSegmentSynchronizerFactory,
	loggers ldlog.Loggers,
) EnvContext {
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     st.EnvMain.Config,
		AllConfig:                     config.Config{},
		BigSegmentStoreFactory:        storeFactory,
		BigSegmentSynchronizerFactory: capturing.create,
		ClientFactory:                 clientFactory,
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		ConnectionMapper: mockConnectionMapper{},
		Loggers:          loggers,
	}, nil)
	require.NoError(t, err)
	return env
}

func nullBigSegmentStoreFactory(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
	return bigsegments.NewNullBigSegmentStore(), nil
}

// TestReanchorBigSegmentSync_StartedSyncContinuesAndOldClosed: once a big segment exists the
// synchronizer is Started; a re-anchor must recreate it on the new key, Start the replacement (so sync
// continues without a gap), and Close the old one.
func TestReanchorBigSegmentSync_StartedSyncContinuesAndOldClosed(t *testing.T) {
	envConfig := st.EnvMain.Config
	capturing := &capturingBigSegmentSynchronizerFactory{}
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	env := newBigSegmentTestEnv(t, nullBigSegmentStoreFactory,
		testclient.FakeLDClientFactoryWithChannel(true, clientCh), capturing, mockLog.Loggers)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	// A big segment appears -> the current synchronizer is Started.
	envImpl.setBigSegmentsExist()
	oldSync := capturing.latest()
	require.True(t, oldSync.isStarted(), "the synchronizer is started once a big segment exists")

	// Re-anchor onto a new key (its client builds healthy, so the re-anchor commits).
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, time.Unix(1000, 0))

	count, sdkKey := capturing.snapshot()
	assert.Equal(t, 2, count, "the synchronizer is recreated on re-anchor")
	assert.Equal(t, reanchorTestKey2, sdkKey, "on the new anchor key")
	newSync := capturing.latest()
	assert.NotSame(t, oldSync, newSync, "a fresh synchronizer instance")
	assert.True(t, newSync.isStarted(), "the replacement is Started so synchronization continues without a gap")
	assert.True(t, oldSync.isClosed(), "the old synchronizer is Closed")

	// Closing the old synchronizer closes its update channel, which is what terminates its
	// update-consumer goroutine (the `for range` in consumeBigSegmentUpdates). Verify the channel is
	// closed so a re-anchor cannot leak a consumer per rotation.
	_, ok := <-oldSync.updateCh
	assert.False(t, ok, "the old synchronizer's update channel is closed, so its consumer goroutine exits")
}

// TestReanchorBigSegmentSync_RollbackDoesNotRewire: if the re-anchor's new client fails to build, the
// re-anchor rolls back and the big-segment synchronizer must stay on the previous anchor (not recreated,
// not closed).
func TestReanchorBigSegmentSync_RollbackDoesNotRewire(t *testing.T) {
	envConfig := st.EnvMain.Config
	capturing := &capturingBigSegmentSynchronizerFactory{}
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	healthy := testclient.FakeLDClientFactoryWithChannel(true, clientCh)
	clientFactory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == reanchorTestKey2 {
			// New anchor fails to init (non-nil uninitialized client + error, as the real SDK returns).
			return &testclient.FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{})}, ld.ErrInitializationFailed
		}
		return healthy(sdkKey, cfg, timeout)
	}

	env := newBigSegmentTestEnv(t, nullBigSegmentStoreFactory, clientFactory, capturing, mockLog.Loggers)
	defer env.Close()
	envImpl := env.(*envContextImpl)
	envImpl.setBigSegmentsExist() // synchronizer started
	oldSync := capturing.latest()

	// Re-anchor to the failing key -> rollback.
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, time.Unix(1000, 0))

	count, sdkKey := capturing.snapshot()
	assert.Equal(t, 1, count, "a rolled-back re-anchor must not recreate the synchronizer")
	assert.Equal(t, envConfig.SDKKey, sdkKey, "it stays on the previous anchor key")
	assert.Same(t, oldSync, capturing.latest(), "same synchronizer instance")
	assert.False(t, oldSync.isClosed(), "the synchronizer is not closed by a rolled-back re-anchor")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "anchor unchanged after rollback")
}

// reanchorTestKey3 is a third anchor SDK key, used to drive A->B->C sequential re-anchors.
const reanchorTestKey3 = config.SDKKey("reanchor-new-anchor-3")

// TestReanchorBigSegmentSync_ReanchorBeforeFirstSegmentThenStartsNewSync covers the ordering where a
// re-anchor happens BEFORE any big segment has appeared (so the replacement is built but not started),
// and then the first segment appears. setBigSegmentsExist must start the CURRENT (new) synchronizer,
// never the retired one.
func TestReanchorBigSegmentSync_ReanchorBeforeFirstSegmentThenStartsNewSync(t *testing.T) {
	envConfig := st.EnvMain.Config
	capturing := &capturingBigSegmentSynchronizerFactory{}
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	env := newBigSegmentTestEnv(t, nullBigSegmentStoreFactory,
		testclient.FakeLDClientFactoryWithChannel(true, clientCh), capturing, mockLog.Loggers)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	oldSync := capturing.latest()
	require.False(t, oldSync.isStarted(), "no big segment yet -> the synchronizer is not started")

	// Re-anchor before any segment appears: the replacement is built on the new key but NOT started.
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, time.Unix(1000, 0))
	newSync := capturing.latest()
	require.NotSame(t, oldSync, newSync, "a fresh synchronizer instance on the new key")
	require.False(t, newSync.isStarted(), "still no segment -> the replacement is not started yet")
	assert.True(t, oldSync.isClosed(), "the old synchronizer is Closed on re-anchor")

	// The first big segment now appears: the CURRENT (new) synchronizer must be started.
	envImpl.setBigSegmentsExist()
	assert.True(t, newSync.isStarted(), "the current synchronizer is started when the first segment appears")
	assert.False(t, oldSync.isStarted(), "the retired synchronizer is never started")
}

// TestReanchorBigSegmentSync_ConcurrentStoreUpdateDuringReanchorIsRaceFree is a regression test for the
// data race introduced when re-anchor made c.bigSegmentSync runtime-mutable: the store-update sink reads
// that field to decide whether to check for big segments, on the SDK data-source goroutine, while a
// re-anchor reassigns it under c.mu on the reconcile goroutine. Without synchronizing the read, `go test
// -race` flags a data race. This test drives the real sink concurrently with a real re-anchor.
func TestReanchorBigSegmentSync_ConcurrentStoreUpdateDuringReanchorIsRaceFree(t *testing.T) {
	envConfig := st.EnvMain.Config
	capturing := &capturingBigSegmentSynchronizerFactory{}
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	env := newBigSegmentTestEnv(t, nullBigSegmentStoreFactory,
		testclient.FakeLDClientFactoryWithChannel(true, clientCh), capturing, mockLog.Loggers)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	// The sink wired into the store adapter; SendAllDataUpdate reads c.bigSegmentSync.
	sink := &envContextStreamUpdates{context: envImpl}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				sink.SendAllDataUpdate(nil) // reads c.bigSegmentSync via bigSegmentSyncConfigured()
			}
		}
	}()

	// Re-anchor concurrently: reanchorBigSegmentSync reassigns c.bigSegmentSync under c.mu.
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, time.Unix(1000, 0))

	close(stop)
	<-done

	// Sanity: the re-anchor committed despite the concurrent sink traffic.
	count, sdkKey := capturing.snapshot()
	assert.Equal(t, 2, count, "the synchronizer was recreated on re-anchor")
	assert.Equal(t, reanchorTestKey2, sdkKey, "on the new anchor key")
}

// TestReanchorBigSegmentSync_RepromoteInGraceFormerAnchorRewires re-anchors A->B, then B->A while A is
// still accepted (an in-grace former anchor whose client was closed when its demotion committed).
// Promoting a previously-accepted key must re-wire big segments identically to a brand-new key: a fresh
// synchronizer bound to A is created and Started (a segment already exists), and B's synchronizer is
// Closed. This pins that the "previously-accepted key" promotion path does not shortcut the big-segment
// re-wire.
//
// It is also the sequential-re-anchor case: three commits produce three synchronizers with every
// intermediate Closed and only the final one current and Started, so synchronizers and their consumer
// goroutines cannot accumulate across rotations. reanchorBigSegmentSync does not branch on whether the
// third key is brand new or a re-promotion, so this covers A->B->C as well.
func TestReanchorBigSegmentSync_RepromoteInGraceFormerAnchorRewires(t *testing.T) {
	envConfig := st.EnvMain.Config
	capturing := &capturingBigSegmentSynchronizerFactory{}
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	env := newBigSegmentTestEnv(t, nullBigSegmentStoreFactory,
		testclient.FakeLDClientFactoryWithChannel(true, clientCh), capturing, mockLog.Loggers)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	envImpl.setBigSegmentsExist()
	syncA := capturing.latest()
	require.True(t, syncA.isStarted(), "the synchronizer is started once a big segment exists")

	// A -> B. A stays accepted in its grace period; its client is closed at commit.
	reanchor(t, env, reanchorTestKey2, envConfig.SDKKey, time.Unix(1000, 0))
	syncB := capturing.latest()
	require.NotSame(t, syncA, syncB)
	require.True(t, syncA.isClosed(), "A's synchronizer is closed after A->B")
	require.True(t, syncB.isStarted(), "B's synchronizer is started (a segment already existed)")

	// B -> A. A is still in the accepted set (its mappings survived the demotion) but has no client, so a
	// fresh client is built and the big-segment sync must be re-wired onto A just like a brand-new key.
	reanchor(t, env, envConfig.SDKKey, reanchorTestKey2, time.Unix(1000, 0))
	syncARepromoted := capturing.latest()
	assert.NotSame(t, syncB, syncARepromoted, "re-promoting A builds a THIRD synchronizer instance")
	assert.NotSame(t, syncA, syncARepromoted, "and a fresh instance, not the retired original A synchronizer")

	count, sdkKey := capturing.snapshot()
	assert.Equal(t, 3, count, "one synchronizer per anchor commit: A, B, A-again")
	assert.Equal(t, envConfig.SDKKey, sdkKey, "the third synchronizer is bound to the re-promoted key A")
	assert.True(t, syncARepromoted.isStarted(), "the re-promoted anchor's synchronizer is Started (a segment exists)")
	assert.True(t, syncB.isClosed(), "B's synchronizer is Closed on the re-promotion")
	assert.Equal(t, envConfig.SDKKey, envImpl.keyRotator.AnchorKey(), "the SDK anchor is back on A")
}
