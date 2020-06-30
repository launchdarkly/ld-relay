package interfaces

import (
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldeval "gopkg.in/launchdarkly/go-server-sdk-evaluation.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
)

// Implementation of ldeval.DataProvider
type dataStoreEvaluatorDataProvider struct {
	store   DataStore
	loggers ldlog.Loggers
}

// NewDataStoreEvaluatorDataProvider provides an adapter for using a DataStore with the Evaluator type
// in go-server-sdk-evaluation.
//
// Normal use of the SDK does not require this type. It is provided for use by other LaunchDarkly
// components that use DataStore and Evaluator separately from the SDK.
func NewDataStoreEvaluatorDataProvider(store DataStore, loggers ldlog.Loggers) ldeval.DataProvider {
	return dataStoreEvaluatorDataProvider{store, loggers}
}

func (d dataStoreEvaluatorDataProvider) GetFeatureFlag(key string) *ldmodel.FeatureFlag {
	item, err := d.store.Get(dataKindFeatures, key)
	if err == nil && item.Item != nil {
		data := item.Item
		if flag, ok := data.(*ldmodel.FeatureFlag); ok {
			return flag
		}
		d.loggers.Errorf("unexpected data type (%T) found in store for feature key: %s. Returning default value", data, key)
	}
	return nil
}

func (d dataStoreEvaluatorDataProvider) GetSegment(key string) *ldmodel.Segment {
	item, err := d.store.Get(dataKindSegments, key)
	if err == nil && item.Item != nil {
		data := item.Item
		if segment, ok := data.(*ldmodel.Segment); ok {
			return segment
		}
		d.loggers.Errorf("unexpected data type (%T) found in store for segment key: %s. Returning default value", data, key)
	}
	return nil
}
