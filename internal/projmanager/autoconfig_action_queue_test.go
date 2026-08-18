package projmanager

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
)

// recordingActions is an AutoConfigActions that records the order in which it was called and can be
// made to block inside a specific environment's action.
type recordingActions struct {
	mu       sync.Mutex
	calls    []string
	allCount int

	// gate, if non-nil for an environment ID, is waited on inside that environment's action.
	gates map[config.EnvironmentID]chan struct{}
	// entered is closed-per-env when that environment's action begins.
	entered map[config.EnvironmentID]chan struct{}
	panicOn config.EnvironmentID
}

func newRecordingActions() *recordingActions {
	return &recordingActions{
		gates:   make(map[config.EnvironmentID]chan struct{}),
		entered: make(map[config.EnvironmentID]chan struct{}),
	}
}

// gateEnv makes the given environment's actions block until the returned release func is called.
// The second return value is closed once the environment's action has actually started.
func (r *recordingActions) gateEnv(id config.EnvironmentID) (release func(), entered <-chan struct{}) {
	gate := make(chan struct{})
	started := make(chan struct{})
	r.mu.Lock()
	r.gates[id] = gate
	r.entered[id] = started
	r.mu.Unlock()
	return func() { close(gate) }, started
}

func (r *recordingActions) enter(id config.EnvironmentID, label string) {
	r.mu.Lock()
	r.calls = append(r.calls, label)
	gate := r.gates[id]
	started := r.entered[id]
	shouldPanic := r.panicOn == id
	r.mu.Unlock()

	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if shouldPanic {
		panic(fmt.Sprintf("boom in %s", id))
	}
	if gate != nil {
		<-gate
	}
}

func (r *recordingActions) AddEnvironment(params envfactory.EnvironmentParams) {
	r.enter(params.EnvID, "add:"+string(params.EnvID)+":"+string(params.Identifiers.FilterKey))
}

func (r *recordingActions) UpdateEnvironment(params envfactory.EnvironmentParams) {
	r.enter(params.EnvID, "update:"+string(params.EnvID)+":"+string(params.Identifiers.FilterKey))
}

func (r *recordingActions) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	r.enter(id, "delete:"+string(id)+":"+string(filter))
}

func (r *recordingActions) ReceivedAllEnvironments() {
	r.mu.Lock()
	r.allCount++
	r.calls = append(r.calls, "all")
	r.mu.Unlock()
}

func (r *recordingActions) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recordingActions) receivedAllCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allCount
}

func envParams(id config.EnvironmentID, filter config.FilterKey) envfactory.EnvironmentParams {
	return envfactory.EnvironmentParams{
		EnvID: id,
		Identifiers: relayenv.EnvIdentifiers{
			EnvKey:    string(id),
			ProjKey:   "proj",
			FilterKey: filter,
		},
	}
}

func newTestQueue(t *testing.T, maxConcurrent int) (*AutoConfigActionQueue, *recordingActions, *ldlogtest.MockLog) {
	t.Helper()
	mockLog := ldlogtest.NewMockLog()
	t.Cleanup(func() { mockLog.DumpIfTestFailed(t) })
	inner := newRecordingActions()
	q := newAutoConfigActionQueue(inner, mockLog.Loggers, maxConcurrent, time.Second)
	t.Cleanup(q.Close)
	return q, inner, mockLog
}

// TestAutoConfigActionQueue_SlowEnvironmentDoesNotBlockOthers is the regression test for the defect this
// type exists to fix: an environment stuck rebuilding its SDK client must not delay an unrelated
// environment's update. Before this queue existed, env B's update ran on the same
// goroutine as env A's action and could not start until A returned.
func TestAutoConfigActionQueue_SlowEnvironmentDoesNotBlockOthers(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	envB := config.EnvironmentID("env-b")

	releaseA, aStarted := inner.gateEnv(envA)
	_, bStarted := inner.gateEnv(envB)

	// Env A's action blocks, standing in for a stalled anchor build.
	q.UpdateEnvironment(envParams(envA, config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env A's action never started")
	}

	// Env B's unrelated update must be processed while A is still stuck.
	q.UpdateEnvironment(envParams(envB, config.DefaultFilter))
	select {
	case <-bStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env B was blocked behind env A's stalled action")
	}

	releaseA()
}

// TestAutoConfigActionQueue_SameEnvironmentStaysOrdered is the other half of the contract: cross-
// environment independence must not cost intra-environment ordering, because RAC actions for one
// environment are order-dependent (an add followed by a delete must not reorder).
func TestAutoConfigActionQueue_SameEnvironmentStaysOrdered(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	releaseA, aStarted := inner.gateEnv(envA)

	q.AddEnvironment(envParams(envA, config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env A's first action never started")
	}

	// Queue two more actions for the same environment while the first is still running.
	q.UpdateEnvironment(envParams(envA, config.DefaultFilter))
	q.DeleteEnvironment(envA, config.DefaultFilter)

	// Nothing else for env A may have run yet.
	assert.Equal(t, []string{"add:env-a:"}, inner.recorded())

	releaseA()

	require.Eventually(t, func() bool {
		return len(inner.recorded()) == 3
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"add:env-a:", "update:env-a:", "delete:env-a:"}, inner.recorded())
}

// TestAutoConfigActionQueue_FilteredEnvironmentsAreIndependent covers the queue-key choice: a filtered
// environment is a separate EnvContext with its own SDK client and its own anchor, so it must not
// wait behind the default environment that shares its environment ID.
func TestAutoConfigActionQueue_FilteredEnvironmentsAreIndependent(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	releaseA, aStarted := inner.gateEnv(envA)

	// The default environment blocks.
	q.UpdateEnvironment(envParams(envA, config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "the default environment's action never started")
	}

	// The same environment ID under a filter key is a different environment, so it must proceed. The
	// gate is keyed by environment ID, so releasing it also releases this one; assert on the record
	// rather than on a second gate.
	q.UpdateEnvironment(envParams(envA, config.FilterKey("filter-1")))
	require.Eventually(t, func() bool {
		return slices.Contains(inner.recorded(), "update:env-a:filter-1")
	}, time.Second, 5*time.Millisecond, "the filtered environment was blocked behind the default one")

	releaseA()
}

// TestAutoConfigActionQueue_ReceivedAllEnvironmentsWaitsForQueuedWork protects the readiness contract:
// the signal sets Relay's fullyConfigured flag, which gates request serving, so it must not fire
// while environments from the same payload are still being applied.
func TestAutoConfigActionQueue_ReceivedAllEnvironmentsWaitsForQueuedWork(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	releaseA, aStarted := inner.gateEnv(envA)

	q.AddEnvironment(envParams(envA, config.DefaultFilter))
	q.AddEnvironment(envParams(config.EnvironmentID("env-b"), config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env A's action never started")
	}

	q.ReceivedAllEnvironments()

	// It must not have fired: env A is still in flight.
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, inner.receivedAllCount(), "readiness fired while an environment was still being applied")

	releaseA()

	require.Eventually(t, func() bool {
		return inner.receivedAllCount() == 1
	}, time.Second, 5*time.Millisecond, "readiness never fired after the queued work drained")
}

// TestAutoConfigActionQueue_ReceivedAllEnvironmentsDoesNotBlockCaller confirms the barrier is asynchronous.
// Blocking the caller would hand head-of-line blocking back for "put" payloads, which is the case
// this type is most needed for.
func TestAutoConfigActionQueue_ReceivedAllEnvironmentsDoesNotBlockCaller(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	releaseA, aStarted := inner.gateEnv(envA)
	defer releaseA()

	q.AddEnvironment(envParams(envA, config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env A's action never started")
	}

	returned := make(chan struct{})
	go func() {
		q.ReceivedAllEnvironments()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		require.FailNow(t, "ReceivedAllEnvironments blocked on a stalled environment")
	}
}

// TestAutoConfigActionQueue_ReceivedAllEnvironmentsWithNoQueuedWork covers the common steady-state case:
// a "put" whose environments were all applied already must still forward the signal.
func TestAutoConfigActionQueue_ReceivedAllEnvironmentsWithNoQueuedWork(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	q.AddEnvironment(envParams(config.EnvironmentID("env-a"), config.DefaultFilter))
	require.Eventually(t, func() bool {
		return len(inner.recorded()) == 1
	}, time.Second, 5*time.Millisecond)

	q.ReceivedAllEnvironments()
	require.Eventually(t, func() bool {
		return inner.receivedAllCount() == 1
	}, time.Second, 5*time.Millisecond)

	// A second signal with nothing touched in between must also forward.
	q.ReceivedAllEnvironments()
	require.Eventually(t, func() bool {
		return inner.receivedAllCount() == 2
	}, time.Second, 5*time.Millisecond)
}

// TestAutoConfigActionQueue_QueuesAreRetired guards against a goroutine leak per environment. Queues are
// created on demand and must be reaped once drained, or a long-lived Relay with churning
// environments accumulates a worker for every environment it has ever seen.
func TestAutoConfigActionQueue_QueuesAreRetired(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	for i := range 20 {
		id := config.EnvironmentID(fmt.Sprintf("env-%d", i))
		q.AddEnvironment(envParams(id, config.DefaultFilter))
		q.DeleteEnvironment(id, config.DefaultFilter)
	}

	require.Eventually(t, func() bool {
		return len(inner.recorded()) == 40
	}, 2*time.Second, 5*time.Millisecond)

	require.Eventually(t, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.queues) == 0
	}, time.Second, 5*time.Millisecond, "drained queues were not retired")
}

// TestAutoConfigActionQueue_QueueRecreatedAfterRetirement exercises the retire/submit handoff: an action
// submitted after a queue has been reaped must start a fresh worker rather than be appended to an
// abandoned queue and silently lost.
func TestAutoConfigActionQueue_QueueRecreatedAfterRetirement(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")

	q.AddEnvironment(envParams(envA, config.DefaultFilter))
	require.Eventually(t, func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.queues) == 0
	}, time.Second, 5*time.Millisecond)

	q.UpdateEnvironment(envParams(envA, config.DefaultFilter))
	require.Eventually(t, func() bool {
		return len(inner.recorded()) == 2
	}, time.Second, 5*time.Millisecond, "an action submitted after the queue retired was lost")
	assert.Equal(t, []string{"add:env-a:", "update:env-a:"}, inner.recorded())
}

// TestAutoConfigActionQueue_ConcurrencyIsBounded confirms the fan-out cap. Without it, a "put" that adds
// every environment at once would construct them all in parallel, where previously they were built
// one at a time on the stream goroutine.
func TestAutoConfigActionQueue_ConcurrencyIsBounded(t *testing.T) {
	const bound = 3
	const envCount = 12

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	var inFlight, maxInFlight atomic.Int64
	release := make(chan struct{})
	blocking := &funcActions{
		add: func(envfactory.EnvironmentParams) {
			cur := inFlight.Add(1)
			for {
				old := maxInFlight.Load()
				if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
		},
	}

	q := newAutoConfigActionQueue(blocking, mockLog.Loggers, bound, time.Second)
	defer q.Close()

	for i := range envCount {
		q.AddEnvironment(envParams(config.EnvironmentID(fmt.Sprintf("env-%d", i)), config.DefaultFilter))
	}

	require.Eventually(t, func() bool {
		return inFlight.Load() == bound
	}, time.Second, 5*time.Millisecond, "expected the bound to be saturated")

	// Give any unbounded work a chance to appear before asserting the ceiling held.
	time.Sleep(50 * time.Millisecond)
	assert.LessOrEqual(t, maxInFlight.Load(), int64(bound), "more actions ran at once than the bound allows")

	close(release)
}

// TestAutoConfigActionQueue_PanicDoesNotFreezeEnvironment covers the containment in execute: an escaped
// panic would kill the worker while leaving its queue in the map, silently freezing that
// environment's configuration for the life of the process.
func TestAutoConfigActionQueue_PanicDoesNotFreezeEnvironment(t *testing.T) {
	q, inner, mockLog := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	inner.mu.Lock()
	inner.panicOn = envA
	inner.mu.Unlock()

	q.AddEnvironment(envParams(envA, config.DefaultFilter))

	// Wait for the recovery log rather than for the recorded call. The recording handler appends the
	// call before it panics, so the call is visible before execute's deferred recover has run -- and
	// asserting on the log at that point races the recover.
	require.Eventually(t, func() bool {
		for _, line := range mockLog.GetOutput(ldlog.Error) {
			if strings.Contains(line, "Panic while applying an auto-configuration action") {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond, "the panic was not caught and logged")

	// The environment must still accept work: the queue was not left orphaned.
	inner.mu.Lock()
	inner.panicOn = ""
	inner.mu.Unlock()

	q.UpdateEnvironment(envParams(envA, config.DefaultFilter))
	require.Eventually(t, func() bool {
		return len(inner.recorded()) == 2
	}, time.Second, 5*time.Millisecond, "the environment stopped processing actions after a panic")
}

// TestAutoConfigActionQueue_CloseRefusesNewWork ensures actions arriving after Close are dropped rather
// than applied against a Relay that is tearing down.
func TestAutoConfigActionQueue_CloseRefusesNewWork(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	inner := newRecordingActions()
	q := newAutoConfigActionQueue(inner, mockLog.Loggers, defaultMaxConcurrentActions, time.Second)

	q.Close()
	q.AddEnvironment(envParams(config.EnvironmentID("env-a"), config.DefaultFilter))
	q.ReceivedAllEnvironments()

	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, inner.recorded(), "actions were applied after Close")
	assert.Zero(t, inner.receivedAllCount())

	// Close is idempotent.
	q.Close()
}

// TestAutoConfigActionQueue_CloseWaitsForQueuedWork confirms the ordinary shutdown path drains rather
// than abandons, so a normal Relay shutdown still finishes applying what it accepted.
func TestAutoConfigActionQueue_CloseWaitsForQueuedWork(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	inner := newRecordingActions()
	q := newAutoConfigActionQueue(inner, mockLog.Loggers, defaultMaxConcurrentActions, time.Second)

	for i := range 10 {
		q.AddEnvironment(envParams(config.EnvironmentID(fmt.Sprintf("env-%d", i)), config.DefaultFilter))
	}
	q.Close()

	assert.Len(t, inner.recorded(), 10, "Close returned before queued actions finished")
}

// TestAutoConfigActionQueue_CloseDoesNotWaitForeverOnStalledWork is the shutdown half of the fix. An
// environment stuck in its anchor build must not hold the process open: Close abandons it once the
// grace period expires, which is safe because the actions re-check Relay's and the environment's
// closed state.
func TestAutoConfigActionQueue_CloseDoesNotWaitForeverOnStalledWork(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	inner := newRecordingActions()
	q := newAutoConfigActionQueue(inner, mockLog.Loggers, defaultMaxConcurrentActions, 50*time.Millisecond)

	envA := config.EnvironmentID("env-a")
	releaseA, aStarted := inner.gateEnv(envA)
	defer releaseA()

	q.AddEnvironment(envParams(envA, config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env A's action never started")
	}

	closed := make(chan struct{})
	go func() {
		q.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Close blocked indefinitely on a stalled action")
	}
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Timed out.*waiting for in-flight environment actions")
}

// TestAutoConfigActionQueue_ReceivedAllEnvironmentsWaitsForEarlierWork pins the barrier's scope. The
// barrier waits on every environment that still has work outstanding, not only on the environments
// seen since the previous signal. That is deliberate: the signal drives Relay's readiness flag, and an
// environment that is still being applied is a reason to wait rather than to report ready.
func TestAutoConfigActionQueue_ReceivedAllEnvironmentsWaitsForEarlierWork(t *testing.T) {
	q, inner, _ := newTestQueue(t, defaultMaxConcurrentActions)

	envA := config.EnvironmentID("env-a")
	releaseA, aStarted := inner.gateEnv(envA)

	// Env A's action starts and stays in flight for the rest of the test.
	q.AddEnvironment(envParams(envA, config.DefaultFilter))
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		require.FailNow(t, "env A's action never started")
	}

	// A first signal must not fire: env A is outstanding.
	q.ReceivedAllEnvironments()
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, inner.receivedAllCount(), "the first signal fired while env A was in flight")

	// A second signal, concerning a different environment, must still wait for env A.
	q.AddEnvironment(envParams(config.EnvironmentID("env-b"), config.DefaultFilter))
	q.ReceivedAllEnvironments()
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, inner.receivedAllCount(), "a later signal must still wait for env A's outstanding work")

	// Once env A drains, both signals forward.
	releaseA()
	require.Eventually(t, func() bool {
		return inner.receivedAllCount() == 2
	}, time.Second, 5*time.Millisecond, "both signals should forward once all work drains")
}

// funcActions is an AutoConfigActions whose behavior is supplied per method; unset methods no-op.
type funcActions struct {
	add         func(envfactory.EnvironmentParams)
	update      func(envfactory.EnvironmentParams)
	del         func(config.EnvironmentID, config.FilterKey)
	receivedAll func()
}

func (f *funcActions) AddEnvironment(params envfactory.EnvironmentParams) {
	if f.add != nil {
		f.add(params)
	}
}

func (f *funcActions) UpdateEnvironment(params envfactory.EnvironmentParams) {
	if f.update != nil {
		f.update(params)
	}
}

func (f *funcActions) DeleteEnvironment(id config.EnvironmentID, filter config.FilterKey) {
	if f.del != nil {
		f.del(id, filter)
	}
}

func (f *funcActions) ReceivedAllEnvironments() {
	if f.receivedAll != nil {
		f.receivedAll()
	}
}
