package metrics

import "sync"

var (
	exporters                   = map[ExporterType]ExporterRegisterer{}
	registerPublicExportersOnce sync.Once
	registerPrivateViewsOnce    sync.Once
)
