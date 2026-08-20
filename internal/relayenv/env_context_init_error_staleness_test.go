package relayenv

// initErr records the outcome of one client build. Nothing rewrites it when the client connects later,
// so GetInitError consults the anchor client's data source before it reports a recorded error.
//
// These tests cover both writers of initErr, startSDKClient and commitReanchor, through the one read
// path they share.

import (
	"errors"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const initErrStaleKeyB = config.SDKKey("init-err-stale-anchor-b")

// staleErrFactory returns an initialized client for every key except targetKey. For targetKey it
// returns a client that has not connected, paired with targetErr, and reports it on targetCh.
func staleErrFactory(
	targetKey config.SDKKey,
	targetErr error,
	healthyCh chan *testclient.FakeLDClient,
	targetCh chan *testclient.FakeLDClient,
) sdks.ClientFactoryFunc {
	healthy := testclient.FakeLDClientFactoryWithChannel(true, healthyCh)
	return func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey != targetKey {
			return healthy(sdkKey, cfg, timeout)
		}
		client := &testclient.FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{})}
		targetCh <- client
		return client, targetErr
	}
}

// TestInitErrStaleness_ReanchorTimeoutClearsOnceDataSourceIsValid covers the commitReanchor writer. A
// re-anchor commits a client that has not connected, so the timeout is recorded. Once that client's
// data source reports Valid, the environment is healthy and GetInitError must stop reporting the
// timeout.
func TestInitErrStaleness_ReanchorTimeoutClearsOnceDataSourceIsValid(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	healthyCh := make(chan *testclient.FakeLDClient, 10)
	targetCh := make(chan *testclient.FakeLDClient, 10)
	factory := staleErrFactory(initErrStaleKeyB, ld.ErrInitializationTimeout, healthyCh, targetCh)

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()
	require.Equal(t, env, requireEnvReady(t, readyCh))
	oldClient := requireClientReady(t, healthyCh)
	require.Eventually(t, func() bool { return env.GetClient() == oldClient }, time.Second, 10*time.Millisecond)

	now := time.Unix(2000, 0)
	reanchorViaReconcile(t, env, initErrStaleKeyB, envConfig.SDKKey, "", envConfig.MobileKey, envConfig.EnvID, now)
	newClient := requireClientReady(t, targetCh)

	// The client has not connected, so the recorded timeout still describes the environment.
	require.Equal(t, ld.ErrInitializationTimeout, env.GetInitError(),
		"a client that has not connected keeps reporting its timeout")

	// An Interrupted data source is not a connected one, so the timeout still stands.
	newClient.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateInterrupted})
	assert.Equal(t, ld.ErrInitializationTimeout, env.GetInitError(),
		"an interrupted data source must not clear the recorded timeout")

	// The SDK connects on its own. The recorded timeout is now stale.
	newClient.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateValid})
	assert.NoError(t, env.GetInitError(),
		"a connected data source means the recorded timeout no longer describes the environment")
}

// TestInitErrStaleness_StartupTimeoutClearsOnceDataSourceIsValid covers the startSDKClient writer. The
// initial build times out, and the same read path must clear the error once that client connects.
func TestInitErrStaleness_StartupTimeoutClearsOnceDataSourceIsValid(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	healthyCh := make(chan *testclient.FakeLDClient, 10)
	targetCh := make(chan *testclient.FakeLDClient, 10)
	// The environment's own key is the target, so the initial build reports a timeout.
	factory := staleErrFactory(envConfig.SDKKey, ld.ErrInitializationTimeout, healthyCh, targetCh)

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()
	require.Equal(t, env, requireEnvReady(t, readyCh))
	client := requireClientReady(t, targetCh)

	require.Equal(t, ld.ErrInitializationTimeout, env.GetInitError(),
		"the initial build's timeout is recorded")

	client.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateValid})
	assert.NoError(t, env.GetInitError(),
		"the same read path clears a startup timeout, so both writers behave alike")
}

// TestInitErrStaleness_FailedIsNotClearedByAnOffDataSource pins the middleware contract. A permanent
// failure must keep reporting, because the middleware rejects requests on that value alone.
func TestInitErrStaleness_FailedIsNotClearedByAnOffDataSource(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	healthyCh := make(chan *testclient.FakeLDClient, 10)
	targetCh := make(chan *testclient.FakeLDClient, 10)
	factory := staleErrFactory(envConfig.SDKKey, ld.ErrInitializationFailed, healthyCh, targetCh)

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()
	require.Equal(t, env, requireEnvReady(t, readyCh))
	client := requireClientReady(t, targetCh)

	client.SetDataSourceStatus(interfaces.DataSourceStatus{State: interfaces.DataSourceStateOff})
	assert.Equal(t, ld.ErrInitializationFailed, env.GetInitError(),
		"an Off data source keeps the permanent failure, so the middleware still rejects requests")
}

// TestInitErrStaleness_NoClientKeepsTheRecordedError guards the nil-client branch. A recorded error
// with no anchor client must still report, rather than reading a nil client's status.
func TestInitErrStaleness_NoClientKeepsTheRecordedError(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.ClientFactoryThatFails(ld.ErrInitializationFailed),
		mockLog.Loggers, make(chan EnvContext, 1))
	defer env.Close()

	envImpl := env.(*envContextImpl)
	require.Eventually(t, func() bool { return envImpl.GetInitError() != nil }, time.Second, 10*time.Millisecond)
	require.Nil(t, env.GetClient(), "sanity: the failing factory installed no client")
	assert.Equal(t, ld.ErrInitializationFailed, env.GetInitError(),
		"with no client there is nothing to prove the error stale")
}

// TestInitErrStaleness_ArbitraryErrorIsNeverCleared pins the boundary a host application depends on.
// A ClientFactoryFunc supplied by a host can return any error, and relay cannot know what it means, so
// the client's own status must not override it. Here the client reports a Valid data source and the
// error still stands.
func TestInitErrStaleness_ArbitraryErrorIsNeverCleared(t *testing.T) {
	envConfig := st.EnvMain.Config
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	hostErr := errors.New("host factory rejected this environment")
	clientCh := make(chan *testclient.FakeLDClient, 10)
	// An initialized fake reports a Valid data source, which is the shape that would wrongly clear the
	// error if the check were not limited to timeouts.
	factory := func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		client, _ := testclient.FakeLDClientFactoryWithChannel(true, clientCh)(sdkKey, cfg, timeout)
		return client, hostErr
	}

	readyCh := make(chan EnvContext, 1)
	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()
	require.Equal(t, env, requireEnvReady(t, readyCh))
	client := requireClientReady(t, clientCh)

	require.Equal(t, interfaces.DataSourceStateValid, client.GetDataSourceStatus().State,
		"sanity: this fixture reports a connected data source")
	assert.Equal(t, hostErr, env.GetInitError(),
		"an error relay does not understand must survive, whatever the client reports")
}
