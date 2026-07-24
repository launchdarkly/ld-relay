package relayenv

// Shared helpers for the re-anchor tests: a driver that re-anchors an environment onto a new SDK
// key, plus recording fakes for the stream-update and big-segment-synchronizer collaborators. These
// are consumed by the re-anchor regression tests in this package (env_context_reanchor_*_test.go)
// and by store_handover_realclient_test.go.

import (
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/require"
)

// reanchorTestKey2 is the "new anchor" SDK key we re-anchor onto. It is deliberately NOT in the real
// sdk-<uuid> credential format so it won't trip secret scanners; relay treats SDK keys as opaque
// non-empty strings, so any value works here.
const reanchorTestKey2 = config.SDKKey("reanchor-new-anchor")

// recordingStreamUpdates is a streams.EnvStreamUpdates that counts the broadcasts it receives, so we
// can observe whether a re-anchor produces duplicate downstream "put"s.
type recordingStreamUpdates struct {
	mu             sync.Mutex
	allDataUpdates int
	singleUpdates  int
	invalidations  int
}

func (r *recordingStreamUpdates) SendAllDataUpdate(_ []ldstoretypes.Collection) {
	r.mu.Lock()
	r.allDataUpdates++
	r.mu.Unlock()
}

func (r *recordingStreamUpdates) SendSingleItemUpdate(_ ldstoretypes.DataKind, _ string, _ ldstoretypes.ItemDescriptor) {
	r.mu.Lock()
	r.singleUpdates++
	r.mu.Unlock()
}

func (r *recordingStreamUpdates) InvalidateClientSideState() {
	r.mu.Lock()
	r.invalidations++
	r.mu.Unlock()
}

func (r *recordingStreamUpdates) allDataCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allDataUpdates
}

// reanchor re-anchors env onto newKey while keeping oldKey accepted for a grace hour (the old
// client stays up while the new one is built, then closes when the commit lands). This mirrors the
// backend's default-rotation behavior: the new anchor is non-expiring, the demoted old anchor
// carries an expiry. It drives the time-injectable reconcileCredentials directly so the
// grace-period math is deterministic.
func reanchor(t *testing.T, env EnvContext, newKey, oldKey config.SDKKey, now time.Time) {
	t.Helper()
	set, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: newKey}).
		WithSDKKey(credential.SDKKeyParams{Value: oldKey, Expiry: util.PtrOrNil(now.Add(time.Hour))}).
		Build()
	require.NoError(t, err)
	env.(*envContextImpl).reconcileCredentials(set, now)
}

// capturingBigSegmentSynchronizerFactory records the SDK key it was constructed with and how many
// times it was invoked, so we can detect whether a re-anchor re-wires big-segment sync.
type capturingBigSegmentSynchronizerFactory struct {
	mu           sync.Mutex
	createCount  int
	lastSDKKey   config.SDKKey
	synchronizer *mockBigSegmentSynchronizer
}

func (f *capturingBigSegmentSynchronizerFactory) create(
	_ httpconfig.HTTPConfig,
	_ bigsegments.BigSegmentStore,
	_ string,
	_ string,
	_ config.EnvironmentID,
	sdkKey config.SDKKey,
	_ ldlog.Loggers,
	_ string,
) bigsegments.BigSegmentSynchronizer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCount++
	f.lastSDKKey = sdkKey
	f.synchronizer = &mockBigSegmentSynchronizer{updateCh: make(chan bigsegments.UpdatesSummary)}
	return f.synchronizer
}

func (f *capturingBigSegmentSynchronizerFactory) snapshot() (int, config.SDKKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCount, f.lastSDKKey
}

// latest returns the most recently created synchronizer (the current one after a re-anchor rebuild).
func (f *capturingBigSegmentSynchronizerFactory) latest() *mockBigSegmentSynchronizer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.synchronizer
}
