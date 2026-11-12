package relayenv

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
)

// countingSDKBigSegmentStore counts the status polls of the SDK big segment store.
type countingSDKBigSegmentStore struct {
	st.NoOpSDKBigSegmentStore
	polls atomic.Int32
}

func (s *countingSDKBigSegmentStore) GetMetadata() (subsystems.BigSegmentStoreMetadata, error) {
	s.polls.Add(1)
	return s.NoOpSDKBigSegmentStore.GetMetadata()
}

func TestReadOnlyStoreDoesNotSynchronizeBigSegmentsButPollsThem(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	redisURL, err := ct.NewOptURLAbsoluteFromString("redis://localhost:6379")
	require.NoError(t, err)
	capturing := &capturingBigSegmentSynchronizerFactory{}
	sdkStore := &countingSDKBigSegmentStore{}

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     st.EnvMain.Config,
		AllConfig:                     config.Config{Redis: config.RedisConfig{URL: redisURL, ReadOnly: true}},
		BigSegmentStoreFactory:        nullBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: capturing.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](sdkStore),
		).StatusPollInterval(10 * time.Millisecond),
		ConnectionMapper: mockConnectionMapper{},
		Loggers:          mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	count, _ := capturing.snapshot()
	assert.Equal(t, 0, count, "no synchronizer is created")

	polls := sdkStore.polls.Load()
	envImpl.setBigSegmentsExist()
	assert.Eventually(t, func() bool { return sdkStore.polls.Load() > polls+1 }, time.Second, 10*time.Millisecond,
		"the SDK polls the big segment store after a big segment appears")
	assert.NotNil(t, envImpl.GetBigSegmentStore(), "the big segment store is still available for reads")
}
