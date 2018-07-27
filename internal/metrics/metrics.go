package metrics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"

	"github.com/pborman/uuid"

	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/logging"
)

type ExporterType string

const (
	datadogExporter     ExporterType = "Datadog"
	stackdriverExporter ExporterType = "Stackdriver"
	prometheusExporter  ExporterType = "Prometheus"

	browser = "browser"
	mobile  = "mobile"
	server  = "server"

	defaultFlushInterval = time.Minute
)

type ExporterOptions interface {
	getType() ExporterType
}

type ExporterRegisterer func(options ExporterOptions) error

type Measure struct {
	measures []*stats.Int64Measure
	tags     *[]tag.Mutator
}

var (
	exporters                   = map[ExporterType]ExporterRegisterer{}
	registerPublicExportersOnce sync.Once
	registerPrivateViewsOnce    sync.Once
	metricsRelayId              string

	relayIdTagKey, _          = tag.NewKey("relayId")
	platformCategoryTagKey, _ = tag.NewKey("platformCategory")
	userAgentTagKey, _        = tag.NewKey("userAgent")
	routeTagKey, _            = tag.NewKey("route")
	methodTagKey, _           = tag.NewKey("method")
	envNameTagKey, _          = tag.NewKey("env")

	// For internal event exporter
	privateConnMeasure    = stats.Int64("_connections", "current number of connections", stats.UnitDimensionless)
	privateNewConnMeasure = stats.Int64("_newconnections", "total number of connections", stats.UnitDimensionless)

	connMeasure    = stats.Int64("connections", "current number of connections", stats.UnitDimensionless)
	newConnMeasure = stats.Int64("newconnections", "total number of connections", stats.UnitDimensionless)
	requestMeasure = stats.Int64("requests", "Number of hits to a route", stats.UnitDimensionless)

	browserTags []tag.Mutator
	mobileTags  []tag.Mutator
	serverTags  []tag.Mutator

	publicTags  = []tag.Key{platformCategoryTagKey, userAgentTagKey, envNameTagKey, routeTagKey, methodTagKey}
	privateTags = []tag.Key{platformCategoryTagKey, userAgentTagKey, relayIdTagKey, envNameTagKey}

	publicConnView     *view.View
	publicNewConnView  *view.View
	requestView        *view.View
	privateConnView    *view.View
	privateNewConnView *view.View

	publicViews  []*view.View
	privateViews []*view.View

	BrowserConns = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: &browserTags}
	MobileConns  = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: &mobileTags}
	ServerConns  = Measure{measures: []*stats.Int64Measure{connMeasure, privateConnMeasure}, tags: &serverTags}

	NewBrowserConns = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: &browserTags}
	NewMobileConns  = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: &mobileTags}
	NewServerConns  = Measure{measures: []*stats.Int64Measure{newConnMeasure, privateNewConnMeasure}, tags: &serverTags}

	BrowserRequests = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: &browserTags}
	MobileRequests  = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: &mobileTags}
	ServerRequests  = Measure{measures: []*stats.Int64Measure{requestMeasure}, tags: &serverTags}
)

func init() {
	metricsRelayId = uuid.New()
	browserTags = append(browserTags, tag.Insert(platformCategoryTagKey, browser))
	mobileTags = append(mobileTags, tag.Insert(platformCategoryTagKey, mobile))
	serverTags = append(serverTags, tag.Insert(platformCategoryTagKey, server))

	publicConnView = &view.View{
		Measure:     connMeasure,
		Aggregation: view.Sum(),
		TagKeys:     publicTags,
	}
	publicNewConnView = &view.View{
		Measure:     newConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     publicTags,
	}
	requestView = &view.View{
		Measure:     requestMeasure,
		Aggregation: view.Count(),
		TagKeys:     publicTags,
	}
	privateConnView = &view.View{
		Measure:     privateConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     privateTags,
	}
	privateNewConnView = &view.View{
		Measure:     privateNewConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     privateTags,
	}
	publicViews = []*view.View{publicConnView, publicNewConnView, requestView}
	privateViews = []*view.View{privateConnView, privateNewConnView}
}

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

type DatadogOptions struct {
	Prefix    string
	TraceAddr *string
	StatsAddr *string
	Tags      []string
}

func (d DatadogOptions) getType() ExporterType {
	return datadogExporter
}

type StackdriverOptions struct {
	Prefix    string
	ProjectID string
}

func (d StackdriverOptions) getType() ExporterType {
	return stackdriverExporter
}

type PrometheusOptions struct {
	Prefix string
	Port   int
}

func (p PrometheusOptions) getType() ExporterType {
	return prometheusExporter
}

func defineExporter(exporterType ExporterType, registerer ExporterRegisterer) {
	exporters[exporterType] = registerer
}

func RegisterExporters(options []ExporterOptions) (registrationErr error) {
	registerPublicExportersOnce.Do(func() {
		for _, o := range options {
			exporter := exporters[o.getType()]
			if exporter == nil {
				registrationErr = fmt.Errorf("Got unexpected exporter type: %s", o.getType())
				return
			} else if err := exporter(o); err != nil {
				registrationErr = fmt.Errorf("Could not register %s exporter: %s", o.getType(), err)
				return
			} else {
				logging.Info.Printf("Successfully registered %s exporter.", o.getType())
			}
		}

		err := view.Register(publicViews...)
		if err != nil {
			registrationErr = fmt.Errorf("Error registering metrics views")
		}
	})
	return registrationErr
}

func registerPrivateViews() (err error) {
	registerPrivateViewsOnce.Do(func() {
		err = view.Register(privateViews...)
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

func WithGauge(ctx context.Context, userAgent string, f func(), measure Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.Error.Printf(`Failed to create tags: %s`, err)
	} else {
		for _, m := range measure.measures {
			ctx, _ := tag.New(ctx, *measure.tags...)
			stats.Record(ctx, m.M(1))
			defer stats.Record(ctx, m.M(-1))
		}
	}
	f()
}

func WithCount(ctx context.Context, userAgent string, f func(), measure Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.Error.Printf(`Failed to create tag for user agent : %s`, err)
	} else {
		for _, m := range measure.measures {
			ctx, _ := tag.New(ctx, *measure.tags...)
			stats.Record(ctx, m.M(1))
		}
	}
	f()
}

// WithRouteCount Records a route hit and starts a trace. For stream connections, the duration of the stream connection is recorded
func WithRouteCount(ctx context.Context, userAgent, route, method string, f func(), measure Measure) {
	tagCtx, err := tag.New(ctx, tag.Insert(routeTagKey, sanitizeTagValue(route)), tag.Insert(methodTagKey, sanitizeTagValue(method)))
	if err != nil {
		logging.Error.Printf(`Failed to create tags for route "%s %s": %s`, method, route, err)
	} else {
		ctx = tagCtx
	}
	ctx, span := trace.StartSpan(ctx, route)
	defer span.End()

	WithCount(ctx, userAgent, f, measure)
}

// Pad empty keys to match tag keyset cardinality since empty strings are dropped
func sanitizeTagValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "_"
	}
	return strings.Replace(v, "/", "_", -1)
}
