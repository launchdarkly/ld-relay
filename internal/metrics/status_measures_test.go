package metrics

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/api"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func intGaugePoints(t *testing.T, m metricdata.Metrics) []metricdata.DataPoint[int64] {
	t.Helper()
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	require.True(t, ok, "%s must be an int64 gauge", m.Name)
	return gauge.DataPoints
}

func floatGaugePoints(t *testing.T, m metricdata.Metrics) []metricdata.DataPoint[float64] {
	t.Helper()
	gauge, ok := m.Data.(metricdata.Gauge[float64])
	require.True(t, ok, "%s must be a float64 gauge", m.Name)
	return gauge.DataPoints
}

// stateValue returns the value of the series for one state, and whether that series exists.
func stateValue(points []metricdata.DataPoint[int64], state string, extra ...attribute.KeyValue) (int64, bool) {
	for _, p := range points {
		if v, ok := p.Attributes.Value(statusStateAttrKey); !ok || v.AsString() != state {
			continue
		}
		if !hasAll(p.Attributes, extra) {
			continue
		}
		return p.Value, true
	}
	return 0, false
}

func hasAll(set attribute.Set, kvs []attribute.KeyValue) bool {
	for _, kv := range kvs {
		if v, ok := set.Value(kv.Key); !ok || v != kv.Value {
			return false
		}
	}
	return true
}

// assertOnlyState asserts that exactly one state series reads 1, that it is the expected one, and
// that every other state is present and reads 0. The zeros matter: a state whose series stopped
// being reported would keep its last value in a backend that holds stale samples, and two states
// would read 1 at once.
func assertOnlyState[T ~string](
	t *testing.T,
	m metricdata.Metrics,
	states []T,
	expected T,
	extra ...attribute.KeyValue,
) {
	t.Helper()
	points := intGaugePoints(t, m)
	for _, state := range states {
		value, found := stateValue(points, string(state), extra...)
		require.True(t, found, "%s must report a series for state %s", m.Name, state)
		if state == expected {
			assert.Equal(t, int64(1), value, "%s state %s", m.Name, state)
		} else {
			assert.Equal(t, int64(0), value, "%s state %s", m.Name, state)
		}
	}
}

func newStatusReader(t *testing.T, source StatusSourceFunc, perEnvironment bool) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	m := &Manager{meter: meter}
	require.NoError(t, m.RegisterStatusObservers(source, perEnvironment))
	return reader
}

const (
	testStateSince  = ldtime.UnixMillisecondTime(1700000000000)
	testErrorTime   = ldtime.UnixMillisecondTime(1700000060000)
	testSyncedOn    = ldtime.UnixMillisecondTime(1700000120000)
	testStateSinceS = 1700000000.0
	testErrorTimeS  = 1700000060.0
	testSyncedOnS   = 1700000120.0
)

// healthyEnvironment is an environment with nothing wrong with it.
func healthyEnvironment(name string) EnvironmentStatusSnapshot {
	return EnvironmentStatusSnapshot{
		Name: name,
		Rep: api.EnvironmentStatusRep{
			EnvID:    "env-id-" + name,
			EnvKey:   "env-key-" + name,
			ProjKey:  "proj-key",
			ProjName: "Proj Name",
			Status:   api.EnvStatusConnected,
			ConnectionStatus: api.ConnectionStatusRep{
				State:      interfaces.DataSourceStateValid,
				StateSince: testStateSince,
			},
			DataStoreStatus: api.DataStoreStatusRep{
				State:      api.DataStoreStateValid,
				StateSince: testStateSince,
				Database:   "redis",
				DBServer:   "redis://localhost:6379",
				DBPrefix:   "ld",
				DBTable:    "flags",
			},
		},
	}
}

func TestStatusObserversReportTheWholeSnapshot(t *testing.T) {
	broken := EnvironmentStatusSnapshot{
		Name: "Broken Env",
		Rep: api.EnvironmentStatusRep{
			Status:         api.EnvStatusDisconnected,
			ExpiringSDKKey: "sdk-***",
			ConnectionStatus: api.ConnectionStatusRep{
				State:      interfaces.DataSourceStateInterrupted,
				StateSince: testStateSince,
				LastError: &api.ConnectionErrorRep{
					Kind:       interfaces.DataSourceErrorKindErrorResponse,
					StatusCode: 503,
					Time:       testErrorTime,
				},
			},
			DataStoreStatus: api.DataStoreStatusRep{
				State:      api.DataStoreStateInterrupted,
				StateSince: testStateSince,
			},
			BigSegmentStatus: &api.BigSegmentStatusRep{
				Available:          true,
				PotentiallyStale:   true,
				LastSynchronizedOn: testSyncedOn,
			},
		},
	}
	snapshot := StatusSnapshot{
		Healthy:      false,
		Environments: []EnvironmentStatusSnapshot{healthyEnvironment("Good Env"), broken},
		AutoConfig: &api.AutoConfigStatusRep{
			State:      interfaces.DataSourceStateValid,
			StateSince: testStateSince,
		},
	}

	reader := newStatusReader(t, func() StatusSnapshot { return snapshot }, true)
	ms := collect(t, reader)

	t.Run("relay level", func(t *testing.T) {
		assertOnlyState(t, ms[statusMeasureName], relayStates, api.StatusDegraded)

		counts := intGaugePoints(t, ms[envCountMeasureName])
		require.Len(t, counts, 1)
		assert.Equal(t, int64(2), counts[0].Value)
	})

	t.Run("counts by state", func(t *testing.T) {
		statuses := intGaugePoints(t, ms[envStatusCountMeasureName])
		connected, _ := stateValue(statuses, api.EnvStatusConnected)
		disconnected, _ := stateValue(statuses, api.EnvStatusDisconnected)
		assert.Equal(t, int64(1), connected)
		assert.Equal(t, int64(1), disconnected)

		conns := intGaugePoints(t, ms[envConnStateCountMeasureName])
		valid, _ := stateValue(conns, string(interfaces.DataSourceStateValid))
		interrupted, _ := stateValue(conns, string(interfaces.DataSourceStateInterrupted))
		off, found := stateValue(conns, string(interfaces.DataSourceStateOff))
		assert.Equal(t, int64(1), valid)
		assert.Equal(t, int64(1), interrupted)
		assert.True(t, found, "a state nothing is in must still be reported")
		assert.Equal(t, int64(0), off)

		stores := intGaugePoints(t, ms[envStoreStateCountMeasureName])
		storeValid, _ := stateValue(stores, api.DataStoreStateValid)
		storeInterrupted, _ := stateValue(stores, api.DataStoreStateInterrupted)
		assert.Equal(t, int64(1), storeValid)
		assert.Equal(t, int64(1), storeInterrupted)

		expiring := intGaugePoints(t, ms[envExpiringKeyCountMeasureName])
		require.Len(t, expiring, 1)
		assert.Equal(t, int64(1), expiring[0].Value)

		unavailable := intGaugePoints(t, ms[envBigSegmentsUnavailableCountMeasureName])
		require.Len(t, unavailable, 1)
		assert.Equal(t, int64(0), unavailable[0].Value)

		stale := intGaugePoints(t, ms[envBigSegmentsStaleCountMeasureName])
		require.Len(t, stale, 1)
		assert.Equal(t, int64(1), stale[0].Value)
	})

	t.Run("auto-configuration stream", func(t *testing.T) {
		assertOnlyState(t, ms[autoConfigStateMeasureName], connectionStates, interfaces.DataSourceStateValid)

		since := floatGaugePoints(t, ms[autoConfigStateSinceMeasureName])
		require.Len(t, since, 1)
		assert.InDelta(t, testStateSinceS, since[0].Value, 0.001)

		_, reported := ms[autoConfigLastErrorMeasureName]
		assert.False(t, reported, "a stream that has not failed reports no last error")
	})

	t.Run("per environment", func(t *testing.T) {
		good := envNameAttrKey.String("Good Env")
		bad := envNameAttrKey.String("Broken Env")

		assertOnlyState(t, ms[envStatusMeasureName], environmentStates, api.EnvStatusConnected, good)
		assertOnlyState(t, ms[envStatusMeasureName], environmentStates, api.EnvStatusDisconnected, bad)
		assertOnlyState(t, ms[envConnStateMeasureName], connectionStates,
			interfaces.DataSourceStateValid, good)
		assertOnlyState(t, ms[envConnStateMeasureName], connectionStates,
			interfaces.DataSourceStateInterrupted, bad)
		assertOnlyState(t, ms[envStoreStateMeasureName], storeStates, api.DataStoreStateValid, good)
		assertOnlyState(t, ms[envStoreStateMeasureName], storeStates, api.DataStoreStateInterrupted, bad)

		expiring := intGaugePoints(t, ms[envExpiringKeyMeasureName])
		goodExpiring, ok := valueWith(expiring, good)
		require.True(t, ok)
		assert.Equal(t, int64(0), goodExpiring, "an environment with no expiring key reports 0, not nothing")
		badExpiring, ok := valueWith(expiring, bad)
		require.True(t, ok)
		assert.Equal(t, int64(1), badExpiring)
	})

	t.Run("last error", func(t *testing.T) {
		points := floatGaugePoints(t, ms[envConnLastErrorMeasureName])
		require.Len(t, points, 1, "only the environment that failed reports a last error")
		assert.InDelta(t, testErrorTimeS, points[0].Value, 0.001)

		kind, ok := points[0].Attributes.Value(errorKindAttrKey)
		require.True(t, ok)
		assert.Equal(t, string(interfaces.DataSourceErrorKindErrorResponse), kind.AsString())

		code, ok := points[0].Attributes.Value(httpResponseStatusAttrKey)
		require.True(t, ok)
		assert.Equal(t, int64(503), code.AsInt64())
	})

	t.Run("big segments", func(t *testing.T) {
		bad := envNameAttrKey.String("Broken Env")

		available, ok := valueWith(intGaugePoints(t, ms[envBigSegmentsAvailMeasureName]), bad)
		require.True(t, ok)
		assert.Equal(t, int64(1), available)

		stale, ok := valueWith(intGaugePoints(t, ms[envBigSegmentsStaleMeasureName]), bad)
		require.True(t, ok)
		assert.Equal(t, int64(1), stale)

		synced := floatGaugePoints(t, ms[envBigSegmentsSyncedMeasureName])
		require.Len(t, synced, 1, "only the environment with a big segment store reports one")
		assert.InDelta(t, testSyncedOnS, synced[0].Value, 0.001)
	})

	t.Run("identity", func(t *testing.T) {
		info := intGaugePoints(t, ms[envInfoMeasureName])
		require.Len(t, info, 2)
		good, ok := pointAttributes(info, envNameAttrKey.String("Good Env"))
		require.True(t, ok)
		assertAttribute(t, good, envIDAttrKey, "env-id-Good Env")
		assertAttribute(t, good, envKeyAttrKey, "env-key-Good Env")
		assertAttribute(t, good, projKeyAttrKey, "proj-key")
		assertAttribute(t, good, projNameAttrKey, "Proj Name")

		// The broken environment is in manual configuration mode, where these are all empty, and an
		// absent attribute value is reported as the sentinel rather than omitted.
		bad, ok := pointAttributes(info, envNameAttrKey.String("Broken Env"))
		require.True(t, ok)
		assertAttribute(t, bad, envIDAttrKey, notProvidedValue)

		store := intGaugePoints(t, ms[envStoreInfoMeasureName])
		require.Len(t, store, 1, "an environment with no persistent store reports no store identity")
		assertAttribute(t, store[0].Attributes, dbSystemAttrKey, "redis")
		assertAttribute(t, store[0].Attributes, serverAddressAttrKey, "redis://localhost:6379")
		assertAttribute(t, store[0].Attributes, storePrefixAttrKey, "ld")
		assertAttribute(t, store[0].Attributes, dbCollectionAttrKey, "flags")
	})
}

func valueWith(points []metricdata.DataPoint[int64], kv attribute.KeyValue) (int64, bool) {
	for _, p := range points {
		if v, ok := p.Attributes.Value(kv.Key); ok && v == kv.Value {
			return p.Value, true
		}
	}
	return 0, false
}

func pointAttributes(points []metricdata.DataPoint[int64], kv attribute.KeyValue) (attribute.Set, bool) {
	for _, p := range points {
		if v, ok := p.Attributes.Value(kv.Key); ok && v == kv.Value {
			return p.Attributes, true
		}
	}
	return *attribute.EmptySet(), false
}

func assertAttribute(t *testing.T, set attribute.Set, key attribute.Key, expected string) {
	t.Helper()
	value, ok := set.Value(key)
	require.True(t, ok, "attribute %s must be reported", key)
	assert.Equal(t, expected, value.AsString(), "attribute %s", key)
}

func TestStatusObserversWithoutPerEnvironmentSeries(t *testing.T) {
	snapshot := StatusSnapshot{
		Healthy:      true,
		Environments: []EnvironmentStatusSnapshot{healthyEnvironment("Good Env")},
	}
	// The big segment rollups are in the always-on tier, so an environment with a stale store must
	// still be counted here.
	snapshot.Environments[0].Rep.BigSegmentStatus = &api.BigSegmentStatusRep{PotentiallyStale: true}

	reader := newStatusReader(t, func() StatusSnapshot { return snapshot }, false)
	ms := collect(t, reader)

	assertOnlyState(t, ms[statusMeasureName], relayStates, api.StatusHealthy)
	stale := intGaugePoints(t, ms[envBigSegmentsStaleCountMeasureName])
	require.Len(t, stale, 1)
	assert.Equal(t, int64(1), stale[0].Value)
	unavailable := intGaugePoints(t, ms[envBigSegmentsUnavailableCountMeasureName])
	require.Len(t, unavailable, 1)
	assert.Equal(t, int64(1), unavailable[0].Value, "a store that could not be read counts as unavailable")

	for _, name := range []string{
		envStatusMeasureName,
		envConnStateMeasureName,
		envConnStateSinceMeasureName,
		envConnLastErrorMeasureName,
		envStoreStateMeasureName,
		envStoreStateSinceMeasureName,
		envBigSegmentsAvailMeasureName,
		envBigSegmentsStaleMeasureName,
		envBigSegmentsSyncedMeasureName,
		envExpiringKeyMeasureName,
		envInfoMeasureName,
		envStoreInfoMeasureName,
	} {
		_, reported := ms[name]
		assert.False(t, reported, "%s must not be registered when per-environment metrics are off", name)
	}
}

func TestStatusObserversOmitAutoConfigOutsideAutoConfigMode(t *testing.T) {
	reader := newStatusReader(t, func() StatusSnapshot {
		return StatusSnapshot{Healthy: true, AutoConfig: nil}
	}, true)
	ms := collect(t, reader)

	for _, name := range []string{
		autoConfigStateMeasureName,
		autoConfigStateSinceMeasureName,
		autoConfigLastErrorMeasureName,
	} {
		_, reported := ms[name]
		assert.False(t, reported, "%s must be absent when Relay is not auto-configured", name)
	}
	assertOnlyState(t, ms[statusMeasureName], relayStates, api.StatusHealthy)
}

func TestStatusObserversReportAutoConfigFailure(t *testing.T) {
	reader := newStatusReader(t, func() StatusSnapshot {
		return StatusSnapshot{
			AutoConfig: &api.AutoConfigStatusRep{
				State:      interfaces.DataSourceStateInitializing,
				StateSince: testStateSince,
				LastError: &api.ConnectionErrorRep{
					Kind: interfaces.DataSourceErrorKindNetworkError,
					Time: testErrorTime,
				},
			},
		}
	}, true)
	ms := collect(t, reader)

	assertOnlyState(t, ms[autoConfigStateMeasureName], connectionStates,
		interfaces.DataSourceStateInitializing)

	points := floatGaugePoints(t, ms[autoConfigLastErrorMeasureName])
	require.Len(t, points, 1)
	assert.InDelta(t, testErrorTimeS, points[0].Value, 0.001)

	code, ok := points[0].Attributes.Value(httpResponseStatusAttrKey)
	require.True(t, ok, "the status code is reported even for a failure with no HTTP response")
	assert.Equal(t, int64(0), code.AsInt64())
}

func TestStatusObserversSkipTimestampsThatAreNotSet(t *testing.T) {
	env := healthyEnvironment("Good Env")
	env.Rep.ConnectionStatus.StateSince = 0
	env.Rep.BigSegmentStatus = &api.BigSegmentStatusRep{Available: true}

	reader := newStatusReader(t, func() StatusSnapshot {
		return StatusSnapshot{Healthy: true, Environments: []EnvironmentStatusSnapshot{env}}
	}, true)
	ms := collect(t, reader)

	_, reported := ms[envConnStateSinceMeasureName]
	assert.False(t, reported, "an unset timestamp is not reported as 1970")
	_, reported = ms[envBigSegmentsSyncedMeasureName]
	assert.False(t, reported, "a big segment store that never synchronized reports no timestamp")

	// The state itself is still reported; only the timestamp is missing.
	assertOnlyState(t, ms[envConnStateMeasureName], connectionStates,
		interfaces.DataSourceStateValid, envNameAttrKey.String("Good Env"))
}

func TestStatusObserversStopReportingARemovedEnvironment(t *testing.T) {
	envs := []EnvironmentStatusSnapshot{healthyEnvironment("First"), healthyEnvironment("Second")}
	reader := newStatusReader(t, func() StatusSnapshot {
		return StatusSnapshot{Healthy: true, Environments: envs}
	}, true)

	require.Len(t, intGaugePoints(t, collect(t, reader)[envInfoMeasureName]), 2)

	envs = envs[:1]
	ms := collect(t, reader)
	info := intGaugePoints(t, ms[envInfoMeasureName])
	require.Len(t, info, 1)
	_, ok := pointAttributes(info, envNameAttrKey.String("Second"))
	assert.False(t, ok, "an environment Relay no longer serves stops being reported")

	counts := intGaugePoints(t, ms[envCountMeasureName])
	require.Len(t, counts, 1)
	assert.Equal(t, int64(1), counts[0].Value)
}

func TestRegisterStatusObserversDoesNothingWithoutAMeter(t *testing.T) {
	m := &Manager{}
	called := false
	require.NoError(t, m.RegisterStatusObservers(func() StatusSnapshot {
		called = true
		return StatusSnapshot{}
	}, true))
	assert.False(t, called, "the source must not be read when OpenTelemetry is disabled")
}

func TestStatusObserversReportAnEnvironmentNameThatIsEmpty(t *testing.T) {
	env := healthyEnvironment("")
	reader := newStatusReader(t, func() StatusSnapshot {
		return StatusSnapshot{Healthy: true, Environments: []EnvironmentStatusSnapshot{env}}
	}, true)
	ms := collect(t, reader)

	assertOnlyState(t, ms[envConnStateMeasureName], connectionStates,
		interfaces.DataSourceStateValid, envNameAttrKey.String(notProvidedValue))
}
