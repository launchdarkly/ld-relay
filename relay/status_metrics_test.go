package relay

import (
	"net/http"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/api"
	"github.com/launchdarkly/ld-relay/v9/internal/metrics"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchStatusDocument asks the status endpoint for the document, so that a test can compare it
// against the snapshot rather than against another copy of the same expectations.
func fetchStatusDocument(t *testing.T, relay *Relay) ldvalue.Value {
	t.Helper()
	r, _ := http.NewRequest("GET", "http://localhost/status", nil)
	result, body := st.DoRequest(r, relay)
	require.Equal(t, http.StatusOK, result.StatusCode)
	return ldvalue.Parse(body)
}

// assertSnapshotMatchesDocument checks the snapshot the instruments observe against the document
// the endpoint serves, field by field. The point of building both from one code path is that they
// cannot disagree; this is what would catch it if they did.
func assertSnapshotMatchesDocument(t *testing.T, relay *Relay) metrics.StatusSnapshot {
	t.Helper()
	document := fetchStatusDocument(t, relay)
	snapshot := relay.statusSnapshot()

	expectedStatus := api.StatusDegraded
	if snapshot.Healthy {
		expectedStatus = api.StatusHealthy
	}
	assert.Equal(t, document.GetByKey("status").StringValue(), expectedStatus,
		"the relay verdict must agree with the document")

	environments := document.GetByKey("environments")
	assert.Equal(t, environments.AsValueMap().Count(), len(snapshot.Environments),
		"the snapshot must hold every environment the document does")

	for _, env := range snapshot.Environments {
		// The document may be keyed by environment ID rather than by the name the snapshot
		// reports, so find the entry by matching one field we know is unique to it.
		entry, found := findEnvironment(environments, env.Rep.ConnectionStatus.State, env.Rep.EnvID)
		require.True(t, found, "no document entry for environment %q", env.Name)

		assert.Equal(t, entry.GetByKey("status").StringValue(), env.Rep.Status)
		assert.Equal(t, entry.GetByKey("connectionStatus").GetByKey("state").StringValue(),
			string(env.Rep.ConnectionStatus.State))
		assert.Equal(t, entry.GetByKey("dataStoreStatus").GetByKey("state").StringValue(),
			env.Rep.DataStoreStatus.State)
		assert.Equal(t, entry.GetByKey("sdkKeys").Count(), len(env.Rep.SDKKeys))
	}
	return snapshot
}

func findEnvironment(
	environments ldvalue.Value,
	state interfaces.DataSourceState,
	envID string,
) (ldvalue.Value, bool) {
	for _, entry := range environments.AsValueMap().AsMap() {
		if entry.GetByKey("envId").StringValue() != envID {
			continue
		}
		if entry.GetByKey("connectionStatus").GetByKey("state").StringValue() != string(state) {
			continue
		}
		return entry, true
	}
	return ldvalue.Null(), false
}

func TestStatusSnapshotAgreesWithTheStatusDocument(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvClientSide, st.EnvMobile)

		withStartedRelay(t, config, func(p relayTestParams) {
			snapshot := assertSnapshotMatchesDocument(t, p.relay)

			assert.True(t, snapshot.Healthy)
			assert.Len(t, snapshot.Environments, 3)
			assert.Nil(t, snapshot.AutoConfig, "there is no auto-config stream in manual mode")

			for _, env := range snapshot.Environments {
				assert.Equal(t, api.EnvStatusConnected, env.Rep.Status)
				assert.Equal(t, interfaces.DataSourceStateValid, env.Rep.ConnectionStatus.State)
			}
		})
	})

	t.Run("degraded by a disconnected environment", func(t *testing.T) {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvMobile)
		config.Main.DisconnectedStatusTime = ct.NewOptDuration(time.Millisecond)

		withStartedRelay(t, config, func(p relayTestParams) {
			env, err := p.relay.getEnvironment(st.EnvMain.Config.SDKKey)
			require.NoError(t, err)
			env.GetClient().(*testclient.FakeLDClient).SetDataSourceStatus(interfaces.DataSourceStatus{
				State:      interfaces.DataSourceStateInterrupted,
				StateSince: time.Now().Add(-time.Minute),
				LastError: interfaces.DataSourceErrorInfo{
					Kind:       interfaces.DataSourceErrorKindErrorResponse,
					StatusCode: 503,
					Time:       time.Now().Add(-time.Minute),
				},
			})

			snapshot := assertSnapshotMatchesDocument(t, p.relay)
			require.False(t, snapshot.Healthy)

			interrupted := findSnapshotEnvironment(t, snapshot, st.EnvMain.Name)
			assert.Equal(t, api.EnvStatusDisconnected, interrupted.Rep.Status)
			assert.Equal(t, interfaces.DataSourceStateInterrupted, interrupted.Rep.ConnectionStatus.State)
			require.NotNil(t, interrupted.Rep.ConnectionStatus.LastError,
				"the error that interrupted the environment must reach the instruments")
			assert.Equal(t, 503, interrupted.Rep.ConnectionStatus.LastError.StatusCode)
		})
	})
}

func TestStatusSnapshotReportsTheDisplayNameInAutoConfigMode(t *testing.T) {
	// The map is built by hand rather than with st.MakeEnvConfigs, which keys by the configured
	// name: both auto-config fixtures share one, so that helper would keep only the last.
	basic, expiring := testEnvBasic.Config, testEnvWithExpiringKey.Config
	config := c.Config{Environment: map[string]*c.EnvConfig{
		"basic":    &basic,
		"expiring": &expiring,
	}}

	withStartedAutoConfigRelay(t, config, func(p relayTestParams) {
		document := fetchStatusDocument(t, p.relay)
		snapshot := assertSnapshotMatchesDocument(t, p.relay)

		// The document is keyed by environment ID in this mode, because a configured name can
		// change. The snapshot reports the display name instead, which is what the request metrics
		// carry, so that status series and traffic series join.
		environments := document.GetByKey("environments")
		assert.True(t, environments.GetByKey(string(testEnvBasic.Config.EnvID)).IsDefined(),
			"the document is keyed by environment ID here")

		basic := findSnapshotEnvironment(t, snapshot, testEnvBasic.ProjName+" "+testEnvBasic.EnvName)
		assert.Equal(t, string(testEnvBasic.Config.EnvID), basic.Rep.EnvID)
		assert.Equal(t, testEnvBasic.EnvKey, basic.Rep.EnvKey)
		assert.Equal(t, testEnvBasic.ProjKey, basic.Rep.ProjKey)

		expiring := findSnapshotEnvironment(t, snapshot,
			testEnvWithExpiringKey.ProjName+" "+testEnvWithExpiringKey.EnvName)
		var expiringCount int
		for _, k := range expiring.Rep.SDKKeys {
			if k.Expiry != nil {
				expiringCount++
			}
		}
		assert.Positive(t, expiringCount, "an expiring key must be visible to the instruments")

		require.NotNil(t, snapshot.AutoConfig, "auto-config mode must report its stream")
		assert.Equal(t, interfaces.DataSourceStateValid, snapshot.AutoConfig.State)
	})
}

func findSnapshotEnvironment(
	t *testing.T,
	snapshot metrics.StatusSnapshot,
	name string,
) metrics.EnvironmentStatusSnapshot {
	t.Helper()
	for _, env := range snapshot.Environments {
		if env.Name == name {
			return env
		}
	}
	names := make([]string, 0, len(snapshot.Environments))
	for _, env := range snapshot.Environments {
		names = append(names, env.Name)
	}
	require.Failf(t, "missing environment", "no environment named %q in %v", name, names)
	return metrics.EnvironmentStatusSnapshot{}
}
