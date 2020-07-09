package metrics

import (
	"sync"

	"github.com/pborman/uuid"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

var (
	exporters                   = map[ExporterType]ExporterRegisterer{}
	registerPublicExportersOnce sync.Once
	registerPrivateViewsOnce    sync.Once
	metricsRelayId              string

	browserTags []tag.Mutator
	mobileTags  []tag.Mutator
	serverTags  []tag.Mutator

	publicConnView     *view.View
	publicNewConnView  *view.View
	requestView        *view.View
	privateConnView    *view.View
	privateNewConnView *view.View
)

func init() {
	metricsRelayId = uuid.New()
	browserTags = append(browserTags, tag.Insert(platformCategoryTagKey, browserTagValue))
	mobileTags = append(mobileTags, tag.Insert(platformCategoryTagKey, mobileTagValue))
	serverTags = append(serverTags, tag.Insert(platformCategoryTagKey, serverTagValue))

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
		TagKeys:     append(publicTags, routeTagKey, methodTagKey),
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
}

func getPublicViews() []*view.View {
	return []*view.View{publicConnView, publicNewConnView, requestView}
}

func getPrivateViews() []*view.View {
	return []*view.View{privateConnView, privateNewConnView}
}
