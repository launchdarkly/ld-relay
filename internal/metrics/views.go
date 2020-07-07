package metrics

import (
	"sync"

	"go.opencensus.io/stats/view"
)

var (
	exporters                   = map[ExporterType]ExporterRegisterer{}
	registerPublicExportersOnce sync.Once
	registerPrivateViewsOnce    sync.Once

	publicConnView     *view.View
	publicNewConnView  *view.View
	requestView        *view.View
	privateConnView    *view.View
	privateNewConnView *view.View
)

func init() {
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
