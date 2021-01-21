package streams

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"

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
