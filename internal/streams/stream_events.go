package streams

import (
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"
)

// This file defines the format for all SSE events published by Relay. Its functions are normally only
// used by the streams package, but they are exported for testing.

var dataKindAPIName = map[ldstoretypes.DataKind]string{ //nolint:gochecknoglobals
	ldstoreimpl.Features(): "flags",
	ldstoreimpl.Segments(): "segments",
}

// We use StringMemoizer for these events because the same event may get broadcast to many connected
// clients, and the SSE server code will call the event's Data() method again for each client-- but
// sometimes there aren't any connected clients at all, in which case we don't want to bother with
// computing a bunch of JSON output.

type deferredEvent struct {
	name   string
	result *util.StringMemoizer
}

func (e deferredEvent) Event() string { return e.name }
func (e deferredEvent) Id() string    { return "" } //nolint:golint,stylecheck
func (e deferredEvent) Data() string  { return e.result.Get() }

// MakeServerSidePutEvent creates a "put" event for server-side SDKs.
func MakeServerSidePutEvent(allData []ldstoretypes.Collection) eventsource.Event {
	return deferredEvent{
		name:   "put",
		result: util.NewStringMemoizer(encodeServerSidePutEventData(allData)),
	}
}

// MakeServerSideFlagsOnlyPutEvent creates a "put" event for old server-side SDKs that use the
// flags-only stream.
func MakeServerSideFlagsOnlyPutEvent(allData []ldstoretypes.Collection) eventsource.Event {
	var flags []ldstoretypes.KeyedItemDescriptor
	for _, coll := range allData {
		if coll.Kind == ldstoreimpl.Features() {
			flags = coll.Items
			break
		}
	}
	return deferredEvent{
		name:   "put",
		result: util.NewStringMemoizer(encodeServerSideFlagsOnlyPutEventData(flags)),
	}
}

// MakeServerSidePatchEvent creates a "patch" event for server-side SDKs.
func MakeServerSidePatchEvent(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) eventsource.Event {
	return deferredEvent{
		name:   "patch",
		result: util.NewStringMemoizer(encodeServerSidePatchEventData(kind, key, item, false)),
	}
}

// MakeServerSideFlagsOnlyPatchEvent creates a "patch" event for old server-side SDKs that use
// the flags-only stream.
func MakeServerSideFlagsOnlyPatchEvent(key string, item ldstoretypes.ItemDescriptor) eventsource.Event {
	return deferredEvent{
		name:   "patch",
		result: util.NewStringMemoizer(encodeServerSidePatchEventData(ldstoreimpl.Features(), key, item, true)),
	}
}

// MakeServerSideDeleteEvent creates a "delete" event for server-side SDKs.
func MakeServerSideDeleteEvent(kind ldstoretypes.DataKind, key string, version int) eventsource.Event {
	return deferredEvent{
		name:   "delete",
		result: util.NewStringMemoizer(encodeServerSideDeleteEventData(kind, key, version, false)),
	}
}

// MakeServerSideFlagsOnlyDeleteEvent creates a "delete" event for old server-side SDKs that use the
// flags-only stream.
func MakeServerSideFlagsOnlyDeleteEvent(key string, version int) eventsource.Event {
	return deferredEvent{
		name:   "delete",
		result: util.NewStringMemoizer(encodeServerSideDeleteEventData(ldstoreimpl.Features(), key, version, true)),
	}
}

// MakePingEvent creates a "ping" event for client-side SDKs.
func MakePingEvent() eventsource.Event {
	return deferredEvent{
		name:   "ping",
		result: util.NewStringMemoizer(func() string { return " " }),
	}
	// We need to send a space for the event data, instead of an empty string; otherwise the data field
	// is not published by eventsource, causing the event to be ignored.
}

func encodeServerSideFlagsOnlyPutEventData(flags []ldstoretypes.KeyedItemDescriptor) func() string {
	return func() string {
		w := jwriter.NewWriter()
		obj := w.Object()
		for _, item := range flags {
			if item.Item.Item == nil {
				obj.Name(item.Key).Null()
			} else {
				ldmodel.MarshalFeatureFlagToJSONWriter(*item.Item.Item.(*ldmodel.FeatureFlag),
					obj.Name(item.Key))
			}
		}
		obj.End()
		return string(w.Bytes())
	}
}

func encodeServerSidePutEventData(allData []ldstoretypes.Collection) func() string {
	if allData == nil {
		allData = []ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: nil},
			{Kind: ldstoreimpl.Segments(), Items: nil},
		}
	}
	return func() string {
		w := jwriter.NewWriter()
		obj := w.Object()
		obj.Name("path").String("/")
		dataObj := obj.Name("data").Object()
		for _, coll := range allData {
			var name string
			switch {
			case coll.Kind == ldstoreimpl.Features():
				name = "flags"
			case coll.Kind == ldstoreimpl.Segments():
				name = "segments"
			default:
				continue
			}
			itemsObj := dataObj.Name(name).Object()
			for _, item := range coll.Items {
				serializeItem(coll.Kind, item.Item, itemsObj.Name(item.Key))
			}
			itemsObj.End()
		}
		dataObj.End()
		obj.End()
		return string(w.Bytes())
	}
}

func encodeServerSidePatchEventData(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
	oldStylePath bool,
) func() string {
	return func() string {
		w := jwriter.NewWriter()
		obj := w.Object()
		obj.Name("path").String(makePath(kind, key, oldStylePath))
		serializeItem(kind, item, obj.Name("data"))
		obj.End()
		return string(w.Bytes())
	}
}

func encodeServerSideDeleteEventData(kind ldstoretypes.DataKind, key string, version int, oldStylePath bool) func() string {
	return func() string {
		w := jwriter.NewWriter()
		obj := w.Object()
		obj.Name("path").String(makePath(kind, key, oldStylePath))
		obj.Name("version").Int(version)
		obj.End()
		return string(w.Bytes())
	}
}

func makePath(kind ldstoretypes.DataKind, key string, oldStylePath bool) string {
	if oldStylePath {
		return "/" + key
	}
	return "/" + dataKindAPIName[kind] + "/" + key
}

func serializeItem(kind ldstoretypes.DataKind, item ldstoretypes.ItemDescriptor, w *jwriter.Writer) {
	switch {
	case item.Item == nil:
		w.Null()
	case kind == ldstoreimpl.Features():
		ldmodel.MarshalFeatureFlagToJSONWriter(*item.Item.(*ldmodel.FeatureFlag), w)
	case kind == ldstoreimpl.Segments():
		ldmodel.MarshalSegmentToJSONWriter(*item.Item.(*ldmodel.Segment), w)
	default:
		w.Null()
	}
}
