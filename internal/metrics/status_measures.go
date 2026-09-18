package metrics

import (
	"context"

	"github.com/launchdarkly/ld-relay/v9/internal/api"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// StatusSourceFunc provides one reading of the state that the status endpoint reports. The
// instruments read from the same snapshot the endpoint serves, so a metric can never describe a
// state the document would not.
//
// This is a function rather than an interface because Relay deliberately exports no methods beyond
// ServeHTTP and Close, and an interface implemented across packages would need an exported one.
type StatusSourceFunc func() StatusSnapshot

// StatusSnapshot is one reading of everything the status endpoint reports.
type StatusSnapshot struct {
	// AutoConfig is the auto-configuration stream's status, or nil when Relay is not in automatic
	// configuration mode.
	AutoConfig *api.AutoConfigStatusRep

	// Environments holds every environment Relay currently serves.
	Environments []EnvironmentStatusSnapshot

	// Healthy is the verdict for Relay as a whole, the same one the document's top-level status
	// field reports.
	Healthy bool
}

// EnvironmentStatusSnapshot is one environment's status, with the name to report it under. That
// name is the environment's display name, which is what the request metrics carry, so that status
// series and traffic series can be joined. It is not always the key the status document uses: in
// automatic configuration mode the document is keyed by environment ID, because a configured name
// can change.
type EnvironmentStatusSnapshot struct {
	Name string
	Rep  api.EnvironmentStatusRep
}

const (
	// stateUnit is the unit for a gauge whose value is 0 or 1. It is an OTel annotation unit, which
	// the Prometheus translation drops.
	//
	// The dimensionless unit "1" cannot be used here. Prometheus renames an instrument carrying it
	// with a _ratio suffix, so launchdarkly.relay.status would scrape as
	// launchdarkly_relay_status_ratio, and none of these are ratios.
	stateUnit = "{state}"

	// environmentUnit is the unit for a gauge that counts environments, or that reports one series
	// per environment. It is also an annotation unit, so it adds no suffix either.
	environmentUnit = "{environment}"

	// timestampUnit is seconds, which Prometheus renders as a _seconds suffix.
	timestampUnit = "s"
)

// The states each field can report. Every one is observed at each collection, including the zeros.
// Observing only the current state would leave the others without a fresh sample, and a backend
// that holds the last sample it saw -- Prometheus holds one for five minutes -- would then show two
// states reading 1 for the same field.
var (
	//nolint:gochecknoglobals
	connectionStates = []interfaces.DataSourceState{
		interfaces.DataSourceStateValid,
		interfaces.DataSourceStateInitializing,
		interfaces.DataSourceStateInterrupted,
		interfaces.DataSourceStateOff,
	}
	//nolint:gochecknoglobals
	storeStates = []string{
		api.DataStoreStateValid,
		api.DataStoreStateInitializing,
		api.DataStoreStateInterrupted,
	}
	//nolint:gochecknoglobals
	relayStates = []string{api.StatusHealthy, api.StatusDegraded}
	//nolint:gochecknoglobals
	environmentStates = []string{api.EnvStatusConnected, api.EnvStatusDisconnected}
)

// statusInstruments holds the instruments the status callback observes. The perEnvironment group is
// nil when the operator has not asked for it, and every method that observes it checks that first.
type statusInstruments struct {
	relayStatus                 otelmetric.Int64ObservableGauge
	envCount                    otelmetric.Int64ObservableGauge
	envStatusCount              otelmetric.Int64ObservableGauge
	envConnStateCount           otelmetric.Int64ObservableGauge
	envStoreStateCount          otelmetric.Int64ObservableGauge
	envExpiringKeyCount         otelmetric.Int64ObservableGauge
	bigSegmentsUnavailableCount otelmetric.Int64ObservableGauge
	bigSegmentsStaleCount       otelmetric.Int64ObservableGauge

	autoConfigState      otelmetric.Int64ObservableGauge
	autoConfigStateSince otelmetric.Float64ObservableGauge
	autoConfigLastError  otelmetric.Float64ObservableGauge

	perEnvironment *environmentStatusInstruments

	// registered is every instrument above, which RegisterCallback wants named up front.
	registered []otelmetric.Observable
}

// environmentStatusInstruments are the instruments that carry an environment name.
type environmentStatusInstruments struct {
	status              otelmetric.Int64ObservableGauge
	connState           otelmetric.Int64ObservableGauge
	connStateSince      otelmetric.Float64ObservableGauge
	connLastError       otelmetric.Float64ObservableGauge
	storeState          otelmetric.Int64ObservableGauge
	storeStateSince     otelmetric.Float64ObservableGauge
	bigSegmentsAvail    otelmetric.Int64ObservableGauge
	bigSegmentsStale    otelmetric.Int64ObservableGauge
	bigSegmentsSyncedOn otelmetric.Float64ObservableGauge
	expiringKey         otelmetric.Int64ObservableGauge
	info                otelmetric.Int64ObservableGauge
	storeInfo           otelmetric.Int64ObservableGauge
}

// RegisterStatusObservers registers the instruments that report the status document, and the one
// callback that observes them. Call it once, after the Relay is constructed. perEnvironment selects
// whether the instruments that carry an environment name are registered at all; without it the
// relay-level counts are still reported, and an environment that is in a state is counted but not
// named.
//
// One callback serves every instrument, from one snapshot, so no two series can describe different
// moments.
func (m *Manager) RegisterStatusObservers(source StatusSourceFunc, perEnvironment bool) error {
	if m.meter == nil {
		return nil // OpenTelemetry is disabled
	}
	instruments, err := newStatusInstruments(m.meter, perEnvironment)
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(_ context.Context, observer otelmetric.Observer) error {
		instruments.observe(observer, source())
		return nil
	}, instruments.observables()...)
	return err
}

func newStatusInstruments(meter otelmetric.Meter, perEnvironment bool) (*statusInstruments, error) {
	b := &instrumentBuilder{meter: meter}
	si := &statusInstruments{
		relayStatus: b.gauge(statusMeasureName, stateUnit,
			"Whether Relay reports itself healthy or degraded, by state"),
		envCount: b.gauge(envCountMeasureName, environmentUnit,
			"The number of environments Relay currently serves"),
		envStatusCount: b.gauge(envStatusCountMeasureName, environmentUnit,
			"The number of environments whose status is connected or disconnected, by state"),
		envConnStateCount: b.gauge(envConnStateCountMeasureName, environmentUnit,
			"The number of environments whose data source is in each state"),
		envStoreStateCount: b.gauge(envStoreStateCountMeasureName, environmentUnit,
			"The number of environments whose data store is in each state"),
		envExpiringKeyCount: b.gauge(envExpiringKeyCountMeasureName, environmentUnit,
			"The number of environments with an expiring SDK key still in service"),
		bigSegmentsUnavailableCount: b.gauge(envBigSegmentsUnavailableCountMeasureName, environmentUnit,
			"The number of environments whose big segment store could not be read"),
		bigSegmentsStaleCount: b.gauge(envBigSegmentsStaleCountMeasureName, environmentUnit,
			"The number of environments whose big segment data is past the staleness threshold"),

		autoConfigState: b.gauge(autoConfigStateMeasureName, stateUnit,
			"The state of the auto-configuration stream, by state. Absent outside automatic configuration mode"),
		autoConfigStateSince: b.timestamp(autoConfigStateSinceMeasureName,
			"When the auto-configuration stream entered its current state, in Unix seconds"),
		autoConfigLastError: b.timestamp(autoConfigLastErrorMeasureName,
			"When the auto-configuration stream last failed, in Unix seconds. Absent until it fails"),
	}

	if perEnvironment {
		si.perEnvironment = &environmentStatusInstruments{
			status: b.gauge(envStatusMeasureName, stateUnit,
				"Whether an environment is connected or disconnected, by state"),
			connState: b.gauge(envConnStateMeasureName, stateUnit,
				"The state of an environment's data source, by state"),
			connStateSince: b.timestamp(envConnStateSinceMeasureName,
				"When an environment's data source entered its current state, in Unix seconds"),
			connLastError: b.timestamp(envConnLastErrorMeasureName,
				"When an environment's data source last failed, in Unix seconds. Absent until it fails"),
			storeState: b.gauge(envStoreStateMeasureName, stateUnit,
				"The state of an environment's data store, by state"),
			storeStateSince: b.timestamp(envStoreStateSinceMeasureName,
				"When an environment's data store entered its current state, in Unix seconds"),
			bigSegmentsAvail: b.gauge(envBigSegmentsAvailMeasureName, stateUnit,
				"Whether an environment's big segment store could be read"),
			bigSegmentsStale: b.gauge(envBigSegmentsStaleMeasureName, stateUnit,
				"Whether an environment's big segment data is past the staleness threshold"),
			bigSegmentsSyncedOn: b.timestamp(envBigSegmentsSyncedMeasureName,
				"When an environment's big segment data was last synchronized, in Unix seconds"),
			expiringKey: b.gauge(envExpiringKeyMeasureName, stateUnit,
				"Whether an environment still serves an expiring SDK key"),
			info: b.gauge(envInfoMeasureName, environmentUnit,
				"Always 1. Carries the environment and project identity, to join the series above against"),
			storeInfo: b.gauge(envStoreInfoMeasureName, environmentUnit,
				"Always 1. Carries the persistent store identity. Absent for an in-memory store"),
		}
	}
	if b.err != nil {
		return nil, b.err
	}
	si.registered = b.all
	return si, nil
}

// instrumentBuilder keeps the first error from a run of instrument constructions, so that the
// declarations above can read as a table instead of interleaving error checks.
type instrumentBuilder struct {
	meter otelmetric.Meter
	err   error
	all   []otelmetric.Observable
}

func (b *instrumentBuilder) gauge(name, unit, description string) otelmetric.Int64ObservableGauge {
	g, err := b.meter.Int64ObservableGauge(name,
		otelmetric.WithDescription(description),
		otelmetric.WithUnit(unit))
	if err != nil && b.err == nil {
		b.err = err
	}
	b.all = append(b.all, g)
	return g
}

// timestamp builds a gauge whose value is a point in time, in Unix seconds. A timestamp is reported
// rather than an age so that the value changes only when the state does. To alert on duration, use
// the current time: time() - launchdarkly_relay_..._state_since_seconds > 300.
func (b *instrumentBuilder) timestamp(name, description string) otelmetric.Float64ObservableGauge {
	g, err := b.meter.Float64ObservableGauge(name,
		otelmetric.WithDescription(description),
		otelmetric.WithUnit(timestampUnit))
	if err != nil && b.err == nil {
		b.err = err
	}
	b.all = append(b.all, g)
	return g
}

// observables returns every instrument the callback observes, which RegisterCallback requires up
// front.
func (si *statusInstruments) observables() []otelmetric.Observable {
	return si.registered
}

func (si *statusInstruments) observe(o otelmetric.Observer, snapshot StatusSnapshot) {
	relayState := api.StatusDegraded
	if snapshot.Healthy {
		relayState = api.StatusHealthy
	}
	observeState(o, si.relayStatus, relayStates, relayState)

	si.observeCounts(o, snapshot)
	si.observeAutoConfig(o, snapshot.AutoConfig)

	if si.perEnvironment == nil {
		return
	}
	for _, env := range snapshot.Environments {
		si.observeEnvironment(o, env)
	}
}

// observeCounts reports how many environments are in each state. These counts are what a Relay
// serving hundreds of environments alerts on, and they are the whole signal when the
// per-environment instruments are not registered.
func (si *statusInstruments) observeCounts(o otelmetric.Observer, snapshot StatusSnapshot) {
	statuses := map[string]int64{}
	connStates := map[interfaces.DataSourceState]int64{}
	storeStateCounts := map[string]int64{}
	var expiringKeys, bigSegmentsUnavailable, bigSegmentsStale int64

	for _, env := range snapshot.Environments {
		statuses[env.Rep.Status]++
		connStates[env.Rep.ConnectionStatus.State]++
		storeStateCounts[env.Rep.DataStoreStatus.State]++
		if env.Rep.ExpiringSDKKey != "" {
			expiringKeys++
		}
		if bs := env.Rep.BigSegmentStatus; bs != nil {
			if !bs.Available {
				bigSegmentsUnavailable++
			}
			if bs.PotentiallyStale {
				bigSegmentsStale++
			}
		}
	}

	o.ObserveInt64(si.envCount, int64(len(snapshot.Environments)))
	observeCountsByState(o, si.envStatusCount, environmentStates, statuses)
	observeCountsByState(o, si.envConnStateCount, connectionStates, connStates)
	observeCountsByState(o, si.envStoreStateCount, storeStates, storeStateCounts)
	o.ObserveInt64(si.envExpiringKeyCount, expiringKeys)
	o.ObserveInt64(si.bigSegmentsUnavailableCount, bigSegmentsUnavailable)
	o.ObserveInt64(si.bigSegmentsStaleCount, bigSegmentsStale)
}

// observeAutoConfig reports the auto-configuration stream. A state other than VALID means Relay has
// stopped learning about environments that are added, deleted, or re-keyed, even though the
// environments it already knows about keep serving flags.
func (si *statusInstruments) observeAutoConfig(o otelmetric.Observer, status *api.AutoConfigStatusRep) {
	if status == nil {
		return // not in automatic configuration mode, so there is no stream to report
	}
	observeState(o, si.autoConfigState, connectionStates, status.State)
	observeTimestamp(o, si.autoConfigStateSince, status.StateSince)
	observeLastError(o, si.autoConfigLastError, status.LastError)
}

func (si *statusInstruments) observeEnvironment(o otelmetric.Observer, env EnvironmentStatusSnapshot) {
	pe := si.perEnvironment
	name := envNameAttrKey.String(sanitizeVerbatimValue(env.Name))
	rep := env.Rep

	observeState(o, pe.status, environmentStates, rep.Status, name)
	observeState(o, pe.connState, connectionStates, rep.ConnectionStatus.State, name)
	observeTimestamp(o, pe.connStateSince, rep.ConnectionStatus.StateSince, name)
	observeLastError(o, pe.connLastError, rep.ConnectionStatus.LastError, name)
	observeState(o, pe.storeState, storeStates, rep.DataStoreStatus.State, name)
	observeTimestamp(o, pe.storeStateSince, rep.DataStoreStatus.StateSince, name)

	observeBool(o, pe.expiringKey, rep.ExpiringSDKKey != "", name)

	if bs := rep.BigSegmentStatus; bs != nil {
		observeBool(o, pe.bigSegmentsAvail, bs.Available, name)
		observeBool(o, pe.bigSegmentsStale, bs.PotentiallyStale, name)
		// An unsynchronized store reports no timestamp at all rather than the zero value, which
		// would read as 1970 on a dashboard. PotentiallyStale already covers that case.
		observeTimestamp(o, pe.bigSegmentsSyncedOn, bs.LastSynchronizedOn, name)
	}

	o.ObserveInt64(pe.info, 1, otelmetric.WithAttributes(
		name,
		envIDAttrKey.String(sanitizeVerbatimValue(rep.EnvID)),
		envKeyAttrKey.String(sanitizeVerbatimValue(rep.EnvKey)),
		projKeyAttrKey.String(sanitizeVerbatimValue(rep.ProjKey)),
		projNameAttrKey.String(sanitizeVerbatimValue(rep.ProjName)),
	))

	if rep.DataStoreStatus.Database != "" {
		o.ObserveInt64(pe.storeInfo, 1, otelmetric.WithAttributes(
			name,
			dbSystemAttrKey.String(sanitizeVerbatimValue(rep.DataStoreStatus.Database)),
			serverAddressAttrKey.String(sanitizeVerbatimValue(rep.DataStoreStatus.DBServer)),
			storePrefixAttrKey.String(sanitizeVerbatimValue(rep.DataStoreStatus.DBPrefix)),
			dbCollectionAttrKey.String(sanitizeVerbatimValue(rep.DataStoreStatus.DBTable)),
		))
	}
}

// observeState reports one series per state the field can hold, reading 1 for the state it is in.
// An unrecognized state -- one the SDK added that Relay has not been taught -- leaves every series
// at 0 rather than being reported under a state of its own, so the sum across states is the signal
// that something is unaccounted for.
func observeState[T ~string](
	o otelmetric.Observer,
	gauge otelmetric.Int64ObservableGauge,
	states []T,
	current T,
	extra ...attribute.KeyValue,
) {
	for _, state := range states {
		var value int64
		if state == current {
			value = 1
		}
		attrs := make([]attribute.KeyValue, 0, len(extra)+1)
		attrs = append(attrs, extra...)
		attrs = append(attrs, statusStateAttrKey.String(string(state)))
		o.ObserveInt64(gauge, value, otelmetric.WithAttributes(attrs...))
	}
}

// observeCountsByState reports a count for every state, including the states nothing is in, so that
// a query can see a state fall to zero rather than watch its series disappear.
func observeCountsByState[T ~string](
	o otelmetric.Observer,
	gauge otelmetric.Int64ObservableGauge,
	states []T,
	counts map[T]int64,
) {
	for _, state := range states {
		o.ObserveInt64(gauge, counts[state], otelmetric.WithAttributes(
			statusStateAttrKey.String(string(state))))
	}
}

func observeBool(
	o otelmetric.Observer,
	gauge otelmetric.Int64ObservableGauge,
	value bool,
	extra ...attribute.KeyValue,
) {
	var n int64
	if value {
		n = 1
	}
	o.ObserveInt64(gauge, n, otelmetric.WithAttributes(extra...))
}

// observeTimestamp reports a point in time as Unix seconds. A time that is not set is not reported
// at all: the zero value is a real timestamp in 1970, and a backend cannot tell the two apart.
func observeTimestamp(
	o otelmetric.Observer,
	gauge otelmetric.Float64ObservableGauge,
	t ldtime.UnixMillisecondTime,
	extra ...attribute.KeyValue,
) {
	if !t.IsDefined() {
		return
	}
	o.ObserveFloat64(gauge, float64(t)/1000, otelmetric.WithAttributes(extra...))
}

// observeLastError reports when a field last failed, with the kind of failure and the HTTP status
// that caused it. A field that has not failed reports no series at all, rather than one reading
// zero.
//
// The status code is always reported, as 0 for a failure that produced no HTTP response, such as a
// network error. Reporting it only when it exists would leave the attribute on some series of this
// instrument and not others, which Prometheus handles poorly.
func observeLastError(
	o otelmetric.Observer,
	gauge otelmetric.Float64ObservableGauge,
	lastError *api.ConnectionErrorRep,
	extra ...attribute.KeyValue,
) {
	if lastError == nil {
		return
	}
	attrs := make([]attribute.KeyValue, 0, len(extra)+2)
	attrs = append(attrs, extra...)
	attrs = append(attrs,
		errorKindAttrKey.String(sanitizeVerbatimValue(string(lastError.Kind))),
		httpResponseStatusAttrKey.Int(lastError.StatusCode))
	o.ObserveFloat64(gauge, float64(lastError.Time)/1000, otelmetric.WithAttributes(attrs...))
}
