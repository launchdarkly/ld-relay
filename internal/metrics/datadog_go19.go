// +build !go1.10

package metrics

import (
	"errors"
)

func init() {
	defineExporter(datadogExporter, registerFailDatadogExporter)
}

func registerFailDatadogExporter(options ExporterOptions) error {
	return errors.New("The Datadog metrics exporter requires Go version 1.10 or greater")
}
