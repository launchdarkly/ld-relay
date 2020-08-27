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

	registerPublicViewsOnce  sync.Once //nolint:gochecknoglobals
	registerPrivateViewsOnce sync.Once //nolint:gochecknoglobals
)

func getPublicViews() []*view.View {
	return []*view.View{publicConnView, publicNewConnView, requestView}
}

func getPrivateViews() []*view.View {
	return []*view.View{privateConnView, privateNewConnView}
}
