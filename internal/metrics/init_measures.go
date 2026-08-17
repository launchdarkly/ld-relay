package metrics

import (
	"context"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"

	otelmetric "go.opentelemetry.io/otel/metric"
)

// InitInstruments records the initialization-delivery limiter's synchronous measurements:
// deliveries with their outcomes, sheds, up-to-date replies, and failed write-deadline
// calls. The observable side of the limiter -- held slots, queue depth, and the admission
// and rejection totals -- comes from RegisterInitConcurrencyObservers instead, which reads
// the limiter's own counters.
type InitInstruments struct {
	deliveries        otelmetric.Int64Counter
	sheds             otelmetric.Int64Counter
	upToDate          otelmetric.Int64Counter
	deadlineSetErrors otelmetric.Int64Counter
}

func newInitInstruments(meter otelmetric.Meter) *InitInstruments {
	deliveries, _ := meter.Int64Counter(initDeliveriesMeasureName,
		otelmetric.WithDescription("Initialization deliveries, by protocol and outcome"),
		otelmetric.WithUnit("{delivery}"))
	sheds, _ := meter.Int64Counter(initShedsMeasureName,
		otelmetric.WithDescription("Requests shed because the initialization budget and queue were full"),
		otelmetric.WithUnit("{request}"))
	upToDate, _ := meter.Int64Counter(initUpToDateMeasureName,
		otelmetric.WithDescription("Stream replays answered with the small up-to-date reply, which is never charged to the budget"),
		otelmetric.WithUnit("{reply}"))
	deadlineSetErrors, _ := meter.Int64Counter(initDeadlineSetErrorsMeasureName,
		otelmetric.WithDescription("Failed attempts to set the connection write deadline; above zero, the deadline protection is not in force"),
		otelmetric.WithUnit("{error}"))
	return &InitInstruments{
		deliveries:        deliveries,
		sheds:             sheds,
		upToDate:          upToDate,
		deadlineSetErrors: deadlineSetErrors,
	}
}

// RecordDelivery records one gated delivery. The outcome is completed, connection_ended, or
// read_error. A connection_ended outcome does not say why the connection ended: a client
// disconnect and a relay deadline cut look the same to the producer.
func (i *InitInstruments) RecordDelivery(protocol, outcome string, capEngaged bool) {
	if i == nil {
		return
	}
	i.deliveries.Add(context.Background(), 1, otelmetric.WithAttributes(
		initProtocolAttrKey.String(protocol),
		initOutcomeAttrKey.String(outcome),
		initCapEngagedAttrKey.Bool(capEngaged),
	))
}

// RecordUpToDate records one up-to-date reply. afterWait is true when the client's basis
// became current while the client waited in the queue.
func (i *InitInstruments) RecordUpToDate(afterWait bool) {
	if i == nil {
		return
	}
	i.upToDate.Add(context.Background(), 1, otelmetric.WithAttributes(
		initAfterWaitAttrKey.Bool(afterWait),
	))
}

// RecordShed records one shed stream replay. Stream sheds carry no environment name: the
// stream repository does not know its environment.
func (i *InitInstruments) RecordShed() {
	if i == nil {
		return
	}
	i.sheds.Add(context.Background(), 1, otelmetric.WithAttributes(
		initTransportAttrKey.String("stream"),
	))
}

// RecordPollShed records one shed polling request, with the environment name.
func (i *InitInstruments) RecordPollShed(envName string) {
	if i == nil {
		return
	}
	attrs := []otelmetric.AddOption{otelmetric.WithAttributes(
		initTransportAttrKey.String("poll"),
		envNameAttrKey.String(sanitizeTagValue(envName)),
	)}
	i.sheds.Add(context.Background(), 1, attrs...)
}

// AddDeadlineSetErrors adds the count of failed write-deadline calls a delivery observed.
func (i *InitInstruments) AddDeadlineSetErrors(n int64) {
	if i == nil || n <= 0 {
		return
	}
	i.deadlineSetErrors.Add(context.Background(), n)
}

// InitInstruments returns the recorder for the initialization-delivery measurements. It is
// nil when OpenTelemetry is disabled; every method of the recorder accepts a nil receiver
// and does nothing, so callers can record without a check.
func (m *Manager) InitInstruments() *InitInstruments {
	return m.initInstruments
}

// RegisterInitConcurrencyObservers registers the observable limiter instruments: the held
// slots, the queue depth, and the admission and rejection totals, with the rejections split
// by cause. Call it one time, after the limiter is built and only when the limiter is
// enabled; the callback reads the limiter's counters at each collection.
func (m *Manager) RegisterInitConcurrencyObservers(limiter *concurrency.Limiter) error {
	if m.meter == nil {
		return nil // OpenTelemetry is disabled
	}
	held, err := m.meter.Int64ObservableGauge(initSlotsHeldMeasureName,
		otelmetric.WithDescription("Initialization-delivery slots currently held"),
		otelmetric.WithUnit("{slot}"))
	if err != nil {
		return err
	}
	waiting, err := m.meter.Int64ObservableGauge(initQueueWaitingMeasureName,
		otelmetric.WithDescription("Requests currently waiting for an initialization-delivery slot"),
		otelmetric.WithUnit("{request}"))
	if err != nil {
		return err
	}
	admitted, err := m.meter.Int64ObservableCounter(initAdmittedMeasureName,
		otelmetric.WithDescription("Requests admitted by the initialization-delivery budget"),
		otelmetric.WithUnit("{request}"))
	if err != nil {
		return err
	}
	rejected, err := m.meter.Int64ObservableCounter(initRejectedMeasureName,
		otelmetric.WithDescription("Requests rejected by the initialization-delivery budget, by cause; only budget_full shows saturation"),
		otelmetric.WithUnit("{request}"))
	if err != nil {
		return err
	}
	_, err = m.meter.RegisterCallback(func(_ context.Context, o otelmetric.Observer) error {
		s := limiter.Stats()
		o.ObserveInt64(held, int64(s.Held))
		o.ObserveInt64(waiting, int64(s.Waiting))
		o.ObserveInt64(admitted, s.Admitted)
		o.ObserveInt64(rejected, s.RejectedFull,
			otelmetric.WithAttributes(initReasonAttrKey.String("budget_full")))
		o.ObserveInt64(rejected, s.RejectedClientGone,
			otelmetric.WithAttributes(initReasonAttrKey.String("client_gone")))
		o.ObserveInt64(rejected, s.RejectedShutdown,
			otelmetric.WithAttributes(initReasonAttrKey.String("shutdown")))
		return nil
	}, held, waiting, admitted, rejected)
	return err
}
