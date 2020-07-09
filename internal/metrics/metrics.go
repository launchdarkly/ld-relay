package metrics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"

	"github.com/launchdarkly/ld-relay/v6/internal/events"
)

type Processor struct {
	OpenCensusCtx context.Context
	closer        chan<- struct{}
	closeOnce     sync.Once
	exporter      *OpenCensusEventsExporter
}

type OptionType interface {
	apply(p *Processor) error
}

type OptionFlushInterval time.Duration

func (o OptionFlushInterval) apply(p *Processor) error {
	return nil
}

type OptionEnvName string

func (o OptionEnvName) apply(p *Processor) error {
	p.OpenCensusCtx, _ = tag.New(p.OpenCensusCtx,
		tag.Insert(envNameTagKey, sanitizeTagValue(string(o))))
	return nil
}

func registerPrivateViews() (err error) {
	registerPrivateViewsOnce.Do(func() {
		err = view.Register(getPrivateViews()...)
		if err != nil {
			err = fmt.Errorf("Error registering metrics views")
		}
	})
	return err
}

func NewMetricsProcessor(publisher events.EventPublisher, options ...OptionType) (*Processor, error) {
	closer := make(chan struct{})
	ctx, _ := tag.New(context.Background(), tag.Insert(relayIdTagKey, metricsRelayId))

	p := &Processor{
		OpenCensusCtx: ctx,
		closer:        closer,
	}

	flushInterval := defaultFlushInterval
	for _, o := range options {
		if err := o.apply(p); err != nil {
			return nil, err
		}
		switch o := o.(type) {
		case OptionFlushInterval:
			flushInterval = time.Duration(o)
		}
	}

	p.exporter = newOpenCensusEventsExporter(metricsRelayId, publisher, flushInterval)
	view.RegisterExporter(p.exporter)

	if err := registerPrivateViews(); err != nil {
		return p, err
	}
	return p, nil
}

func (p *Processor) Close() {
	p.closeOnce.Do(func() {
		view.UnregisterExporter(p.exporter)
		p.exporter.close()
		close(p.closer)
	})
}

// Pad empty keys to match tag keyset cardinality since empty strings are dropped
func sanitizeTagValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "_"
	}
	return strings.Replace(v, "/", "_", -1)
}
