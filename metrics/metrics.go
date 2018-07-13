package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/pborman/uuid"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"

	"gopkg.in/launchdarkly/ld-relay.v5/events"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
)

type Measure struct {
	measure *stats.Int64Measure
	tags    *[]tag.Mutator
}

var (
	relayIdTagKey          tag.Key
	platformCategoryTagKey tag.Key
	userAgentTagKey        tag.Key
)

const (
	browser = "browser"
	mobile  = "mobile"
	server  = "server"

	defaultFlushInterval = time.Minute
)

func init() {
	relayIdTagKey, _ = tag.NewKey("relayId")
	platformCategoryTagKey, _ = tag.NewKey("platformCategory")
	userAgentTagKey, _ = tag.NewKey("userAgent")

	browserTags = append(browserTags, tag.Insert(platformCategoryTagKey, browser))
	mobileTags = append(mobileTags, tag.Insert(platformCategoryTagKey, mobile))
	serverTags = append(serverTags, tag.Insert(platformCategoryTagKey, server))
}

var (
	connMeasure    = stats.Int64("launchdarkly/relay/measures/connections", "Number of current connections", "connections")
	newConnMeasure = stats.Int64("launchdarkly/relay/measures/newconnections", "Number of new connections", "connections")

	browserTags []tag.Mutator
	mobileTags  []tag.Mutator
	serverTags  []tag.Mutator
)

var (
	BrowserConns = Measure{measure: connMeasure, tags: &browserTags}
	MobileConns  = Measure{measure: connMeasure, tags: &mobileTags}
	ServerConns  = Measure{measure: connMeasure, tags: &serverTags}

	NewBrowserConns = Measure{measure: newConnMeasure, tags: &browserTags}
	NewMobileConns  = Measure{measure: newConnMeasure, tags: &mobileTags}
	NewServerConns  = Measure{measure: newConnMeasure, tags: &serverTags}
)

type Processor struct {
	OpenCensusCtx context.Context
	connView      *view.View
	newConnView   *view.View
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

func NewMetricsProcessor(publisher events.EventPublisher, options ...OptionType) (*Processor, error) {
	closer := make(chan struct{})
	relayId := uuid.New()
	tags := []tag.Key{platformCategoryTagKey, userAgentTagKey, relayIdTagKey}

	ctx, _ := tag.New(context.Background(),
		tag.Insert(relayIdTagKey, relayId),
	)

	connView := &view.View{
		Measure:     connMeasure,
		Aggregation: view.Sum(),
		TagKeys:     tags,
	}

	newConnView := &view.View{
		Measure:     newConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     tags,
	}

	p := &Processor{
		OpenCensusCtx: ctx,
		connView:      connView,
		newConnView:   newConnView,
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

	if err := view.Register(connView, newConnView); err != nil {
		return nil, err
	}

	p.exporter = newOpenCensusEventsExporter(relayId, publisher, flushInterval)
	view.RegisterExporter(p.exporter)

	return p, nil
}

func (p *Processor) Close() {
	p.closeOnce.Do(func() {
		view.Unregister(p.connView, p.newConnView)
		view.UnregisterExporter(p.exporter)
		p.exporter.close()
		close(p.closer)
	})
}

func WithGauge(ctx context.Context, userAgent string, f func(), measures ...Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, userAgent))
	if err != nil {
		logging.Error.Printf(`Failed to create tag for user agent "%s": %s`, userAgent, err)
		return
	}
	for _, m := range measures {
		ctx, _ := tag.New(ctx, *m.tags...)
		stats.Record(ctx, m.measure.M(1))
		defer stats.Record(ctx, m.measure.M(-1))
	}
	f()
}

func WithCount(ctx context.Context, userAgent string, f func(), measures ...Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, userAgent))
	if err != nil {
		logging.Error.Printf(`Failed to create tag for user agent "%s": %s`, userAgent, err)
		return
	}
	for _, m := range measures {
		ctx, _ := tag.New(ctx, *m.tags...)
		stats.Record(ctx, m.measure.M(1))
	}
	f()
}
