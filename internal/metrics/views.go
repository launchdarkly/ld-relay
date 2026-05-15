package metrics

import (
	"sync"

	"go.opencensus.io/stats/view"
)

var (
	publicConnView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     connMeasure,
		Aggregation: view.Sum(),
		TagKeys:     publicTags,
	}
	publicNewConnView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     newConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     publicTags,
	}
	requestView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     requestMeasure,
		Aggregation: view.Count(),
		TagKeys:     append(publicTags, routeTagKey, methodTagKey),
	}
	requestDurationView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     requestDurationMeasure,
		Aggregation: view.Distribution(10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1000000),
		TagKeys:     append(publicTags, routeTagKey, methodTagKey),
	}
	privateConnView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     privateConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     privateTags,
	}
	privateNewConnView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     privateNewConnMeasure,
		Aggregation: view.Sum(),
		TagKeys:     privateTags,
	}
	privatePollingRequestsView *view.View = &view.View{ //nolint:gochecknoglobals
		Measure:     privatePollingRequestsMeasure,
		Aggregation: view.Sum(),
		TagKeys:     privateTags,
	}

	registerPublicViewsOnce  sync.Once //nolint:gochecknoglobals
	registerPrivateViewsOnce sync.Once //nolint:gochecknoglobals
)

func getPublicViews() []*view.View {
	return []*view.View{publicConnView, publicNewConnView, requestView, requestDurationView}
}

func getPrivateViews() []*view.View {
	return []*view.View{privateConnView, privateNewConnView, privatePollingRequestsView}
}
