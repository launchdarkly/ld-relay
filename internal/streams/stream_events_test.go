package streams

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
)

func TestMakeServerSidePutEvents(t *testing.T) {
	allData := []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testFlag1.Key, Item: sharedtest.FlagDesc(testFlag1)},
				{Key: testFlag2.Key, Item: sharedtest.FlagDesc(testFlag2)},
			},
		},
		{
			Kind: ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testSegment1.Key, Item: sharedtest.SegmentDesc(testSegment1)},
			},
		},
		{
			Kind: ldstoreimpl.ConfigOverrides(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testIndexSamplingOverride.Key, Item: sharedtest.ConfigOverrideDesc(testIndexSamplingOverride)},
			},
		},
		{
			Kind: ldstoreimpl.Metrics(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testMetric1.Key, Item: sharedtest.MetricDesc(testMetric1)},
			},
		},
	}

	t.Run("all stream", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/",
	"data": {
		"flags": {
			"flag1": ` + string(testFlag1JSON) + `,
			"flag2": ` + string(testFlag2JSON) + `
		},
		"segments": {
			"segment1": ` + string(testSegment1JSON) + `
		},
		"configurationOverrides": {
			"indexSamplingRatio": ` + string(testIndexSamplingOverrideJSON) + `
		},
		"metrics": {
			"metric1": ` + string(testMetric1JSON) + `
		}
	}
}`

		event := MakeServerSidePutEvent(allData)

		assert.Equal(t, "put", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("flags stream", func(t *testing.T) {
		expectedJSON := `
{
	"flag1": ` + string(testFlag1JSON) + `,
	"flag2": ` + string(testFlag2JSON) + `
}`

		event := MakeServerSideFlagsOnlyPutEvent(allData)

		assert.Equal(t, "put", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})
}

func TestServerSidePatchEvents(t *testing.T) {
	t.Run("all stream - flag", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/flags/flag1",
	"data": ` + string(testFlag1JSON) + `
}`
		event := MakeServerSidePatchEvent(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
		assert.Equal(t, "patch", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("all stream - segment", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/segments/segment1",
	"data": ` + string(testSegment1JSON) + `
}`
		event := MakeServerSidePatchEvent(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))
		assert.Equal(t, "patch", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("all stream - config override", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/configurationOverrides/indexSamplingRatio",
	"data": ` + string(testIndexSamplingOverrideJSON) + `
}`
		event := MakeServerSidePatchEvent(ldstoreimpl.ConfigOverrides(), testIndexSamplingOverride.Key, sharedtest.ConfigOverrideDesc(testIndexSamplingOverride))
		assert.Equal(t, "patch", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("all stream - metric", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/metrics/metric1",
	"data": ` + string(testMetric1JSON) + `
}`
		event := MakeServerSidePatchEvent(ldstoreimpl.Metrics(), testMetric1.Key, sharedtest.MetricDesc(testMetric1))
		assert.Equal(t, "patch", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("flags stream", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/flag1",
	"data": ` + string(testFlag1JSON) + `
}`
		event := MakeServerSideFlagsOnlyPatchEvent(testFlag1.Key, sharedtest.FlagDesc(testFlag1))
		assert.Equal(t, "patch", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

}

func TestServerSideDeleteEvents(t *testing.T) {
	t.Run("all stream - flag", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/flags/flag1",
	"version": 1
}`
		event := MakeServerSideDeleteEvent(ldstoreimpl.Features(), "flag1", 1)
		assert.Equal(t, "delete", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("all stream - segment", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/segments/segment1",
	"version": 1
}`
		event := MakeServerSideDeleteEvent(ldstoreimpl.Segments(), "segment1", 1)
		assert.Equal(t, "delete", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("all stream - config override", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/configurationOverrides/indexSamplingRatio",
	"version": 1
}`
		event := MakeServerSideDeleteEvent(ldstoreimpl.ConfigOverrides(), "indexSamplingRatio", 1)
		assert.Equal(t, "delete", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("all stream - metric", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/metrics/metric1",
	"version": 1
}`
		event := MakeServerSideDeleteEvent(ldstoreimpl.Metrics(), "metric1", 1)
		assert.Equal(t, "delete", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})

	t.Run("flags stream", func(t *testing.T) {
		expectedJSON := `
{
	"path": "/flag1",
	"version": 1
}`
		event := MakeServerSideFlagsOnlyDeleteEvent("flag1", 1)
		assert.Equal(t, "delete", event.Event())
		assert.JSONEq(t, expectedJSON, event.Data())
		assert.Equal(t, "", event.Id())
	})
}

func TestMakePingEvent(t *testing.T) {
	event := MakePingEvent()
	assert.Equal(t, "ping", event.Event())
	assert.Equal(t, " ", event.Data())
	assert.Equal(t, "", event.Id())
}
