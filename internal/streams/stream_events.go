package streams

import (
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-jsonstream/v4/jwriter"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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
func (e deferredEvent) Id() string    { return "" } //nolint:revive
func (e deferredEvent) Data() string  { return e.result.Get() }

// fdv2Event is a pre-rendered SSE event for the FDv2 protocol. The data string is
// encoded exactly once, when the event is constructed.
type fdv2Event struct {
	name subsystems.EventName
	data string
}

func (e fdv2Event) Event() string { return string(e.name) }
func (e fdv2Event) Id() string    { return "" } //nolint:revive
func (e fdv2Event) Data() string  { return e.data }

// The encoders below produce the same JSON as marshaling the corresponding
// subsystems event types (ServerIntent, PutObject, DeleteObject, Selector), but in a
// single jwriter pass. In particular, a change's pre-serialized object is embedded
// verbatim instead of being re-parsed and compacted by encoding/json.

func makeServerIntentEvent(payload subsystems.Payload) eventsource.Event {
	w := jwriter.NewWriter()
	obj := w.Object()
	payloads := obj.Name("payloads").Array()
	payloadObj := payloads.Object()
	payloadObj.Name("id").String(payload.ID)
	payloadObj.Name("target").Int(payload.Target)
	payloadObj.Name("intentCode").String(string(payload.Code))
	payloadObj.Name("reason").String(payload.Reason)
	payloadObj.End()
	payloads.End()
	obj.End()
	return fdv2Event{name: subsystems.EventServerIntent, data: string(w.Bytes())}
}

func makePutObjectEvent(change subsystems.Change) eventsource.Event {
	w := jwriter.NewWriter()
	obj := w.Object()
	obj.Name("version").Int(change.Version)
	obj.Name("kind").String(string(change.Kind))
	obj.Name("key").String(change.Key)
	if len(change.Object) == 0 {
		obj.Name("object").Null()
	} else {
		obj.Name("object").Raw(change.Object)
	}
	obj.End()
	return fdv2Event{name: subsystems.EventPutObject, data: string(w.Bytes())}
}

func makeDeleteObjectEvent(change subsystems.Change) eventsource.Event {
	w := jwriter.NewWriter()
	obj := w.Object()
	obj.Name("version").Int(change.Version)
	obj.Name("kind").String(string(change.Kind))
	obj.Name("key").String(change.Key)
	obj.End()
	return fdv2Event{name: subsystems.EventDeleteObject, data: string(w.Bytes())}
}

func makePayloadTransferredEvent(selector subsystems.Selector) eventsource.Event {
	w := jwriter.NewWriter()
	obj := w.Object()
	obj.Name("state").String(selector.State())
	obj.Name("version").Int(selector.Version())
	obj.End()
	return fdv2Event{name: subsystems.EventPayloadTransferred, data: string(w.Bytes())}
}

func MakeEventsForUpToDate(selector subsystems.Selector) []eventsource.Event {
	return []eventsource.Event{makeServerIntentEvent(subsystems.Payload{
		ID:     selector.State(),
		Target: selector.Version(),
		Code:   subsystems.IntentNone,
		Reason: "up-to-date",
	})}
}

func MakeEventsForSetBasis(changes []subsystems.Change, selector subsystems.Selector) []eventsource.Event {
	events := make([]eventsource.Event, 0, len(changes)+2)
	events = append(events, makeServerIntentEvent(subsystems.Payload{
		ID:     selector.State(),
		Target: selector.Version(),
		Code:   subsystems.IntentTransferFull,
		Reason: "cant-catchup",
	}))

	for _, change := range changes {
		// NOTE: We don't have to worry about delete events here since this is
		// meant as a full replacement.
		if change.Action == subsystems.ChangeTypePut {
			events = append(events, makePutObjectEvent(change))
		}
	}

	events = append(events, makePayloadTransferredEvent(selector))
	return events
}

func MakeEventsForApplyDelta(changes []subsystems.Change, selector subsystems.Selector) []eventsource.Event {
	events := make([]eventsource.Event, 0, len(changes)+1)
	for _, change := range changes {
		switch change.Action {
		case subsystems.ChangeTypePut:
			events = append(events, makePutObjectEvent(change))
		case subsystems.ChangeTypeDelete:
			events = append(events, makeDeleteObjectEvent(change))
		}
	}

	events = append(events, makePayloadTransferredEvent(selector))
	return events
}

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
