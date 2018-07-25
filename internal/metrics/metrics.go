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
)

type ExporterOptions interface {
	getType() ExporterType
}

type ExporterRegisterer func(options ExporterOptions) error

var (
	exporters                   = map[ExporterType]ExporterRegisterer{}
	registerPublicExportersOnce sync.Once
	registerViewsOnce           sync.Once
	metricsRelayId              string
)

type Measure struct {
	measure *stats.Int64Measure
	tags    *[]tag.Mutator
}

var (
	relayIdTagKey, _          = tag.NewKey("relayId")
	platformCategoryTagKey, _ = tag.NewKey("platformCategory")
	userAgentTagKey, _        = tag.NewKey("userAgent")
	routeTagKey, _            = tag.NewKey("route")
	methodTagKey, _           = tag.NewKey("method")
	envNameTagKey, _          = tag.NewKey("env")
)

const (
	browser = "browser"
	mobile  = "mobile"
	server  = "server"

	defaultFlushInterval = time.Minute
)

func init() {
	metricsRelayId = uuid.New()
	browserTags = append(browserTags, tag.Insert(platformCategoryTagKey, browser))
	mobileTags = append(mobileTags, tag.Insert(platformCategoryTagKey, mobile))
	serverTags = append(serverTags, tag.Insert(platformCategoryTagKey, server))
}

var (
	connMeasure    = stats.Int64("connections", "current number of connections", stats.UnitDimensionless)
	newConnMeasure = stats.Int64("newconnections", "total number of connections", stats.UnitDimensionless)
	requestMeasure = stats.Int64("requests", "Number of hits to a route", stats.UnitDimensionless)

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

	BrowserRequests = Measure{measure: requestMeasure, tags: &browserTags}
	MobileRequests  = Measure{measure: requestMeasure, tags: &mobileTags}
	ServerRequests  = Measure{measure: requestMeasure, tags: &serverTags}
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

type DatadogOptions struct {
	Prefix    string
	TraceAddr *string
	StatsAddr *string
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

		tags := []tag.Key{platformCategoryTagKey, userAgentTagKey, relayIdTagKey, envNameTagKey, routeTagKey, methodTagKey}

		err := view.Register(&view.View{
			Measure:     requestMeasure,
			Aggregation: view.Count(),
			TagKeys:     tags,
		})
		if err != nil {
			registrationErr = fmt.Errorf("Error registering metrics views")
		}
	})
	return registrationErr
}

func registerViews() (err error) {
	registerViewsOnce.Do(func() {
		tags := []tag.Key{platformCategoryTagKey, userAgentTagKey, relayIdTagKey, envNameTagKey}

		views := []*view.View{
			&view.View{
				Measure:     connMeasure,
				Aggregation: view.Sum(),
				TagKeys:     tags,
			},
			&view.View{
				Measure:     newConnMeasure,
				Aggregation: view.Sum(),
				TagKeys:     tags,
			}}

		err = view.Register(views...)
		if err != nil {
			err = fmt.Errorf("Error registering metrics views")
		}
	})
	return err
}

func NewMetricsProcessor(publisher events.EventPublisher, options ...OptionType) (*Processor, error) {
	closer := make(chan struct{})
	fmt.Println(metricsRelayId)
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

	if err := registerViews(); err != nil {
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

func WithGauge(ctx context.Context, userAgent string, f func(), measures ...Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.Error.Printf(`Failed to create tags: %s`, err)
	} else {
		for _, m := range measures {
			ctx, _ := tag.New(ctx, *m.tags...)
			stats.Record(ctx, m.measure.M(1))
			defer stats.Record(ctx, m.measure.M(-1))
		}
	}
	f()
}

func WithCount(ctx context.Context, userAgent string, f func(), measures ...Measure) {
	ctx, err := tag.New(ctx, tag.Insert(userAgentTagKey, sanitizeTagValue(userAgent)))
	if err != nil {
		logging.Error.Printf(`Failed to create tag for user agent : %s`, err)
	} else {
		for _, m := range measures {
			ctx, _ := tag.New(ctx, *m.tags...)
			stats.Record(ctx, m.measure.M(1))
		}
	}
	f()
}

// WithRouteCount Records a route hit and starts a trace. For stream connections, the duration of the stream connection is recorded
func WithRouteCount(ctx context.Context, userAgent, route, method string, f func(), measures ...Measure) {
	tagCtx, err := tag.New(ctx, tag.Insert(routeTagKey, sanitizeTagValue(route)), tag.Insert(methodTagKey, sanitizeTagValue(method)))
	if err != nil {
		logging.Error.Printf(`Failed to create tags for route "%s %s": %s`, method, route, err)
	} else {
		ctx = tagCtx
	}
	ctx, span := trace.StartSpan(ctx, route)
	defer span.End()

	WithCount(ctx, userAgent, f, measures...)
}

// Pad empty keys to match tag keyset cardinality since empty strings are dropped
func sanitizeTagValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "_"
	}
	return strings.Replace(v, "/", "_", -1)
}
