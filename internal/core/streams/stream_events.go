package streams

import (
	"encoding/json"

	"github.com/launchdarkly/eventsource"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

// This file defines the format for all SSE events published by Relay. Its functions are normally only
// used by the streams package, but they are exported for testing.

var dataKindAPIName = map[ldstoretypes.DataKind]string{ //nolint:gochecknoglobals
	ldstoreimpl.Features(): "flags",
	ldstoreimpl.Segments(): "segments",
}

type flagsPutEvent map[string]interface{}
type allPutEvent struct {
	D map[string]map[string]interface{} `json:"data"`
}
type deleteEvent struct {
	Path    string `json:"path"`
	Version int    `json:"version"`
}

type upsertEvent struct {
	Path string      `json:"path"`
	D    interface{} `json:"data"`
}

type pingEvent struct{}

// MakeServerSidePutEvent creates a "put" event for server-side SDKs.
func MakeServerSidePutEvent(allData []ldstoretypes.Collection) eventsource.Event {
	var allDataMap = map[string]map[string]interface{}{
		"flags":    {},
		"segments": {},
	}
	for _, coll := range allData {
		name := dataKindAPIName[coll.Kind]
		for _, item := range coll.Items {
			allDataMap[name][item.Key] = item.Item.Item
		}
	}
	return allPutEvent{D: allDataMap}
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
	flagsMap := make(map[string]interface{}, len(flags))
	for _, f := range flags {
		flagsMap[f.Key] = f.Item.Item
	}
	return flagsPutEvent(flagsMap)
}

// MakeServerSidePatchEvent creates a "patch" event for server-side SDKs.
func MakeServerSidePatchEvent(
	kind ldstoretypes.DataKind,
	key string,
	item ldstoretypes.ItemDescriptor,
) eventsource.Event {
	return upsertEvent{
		Path: "/" + dataKindAPIName[kind] + "/" + key,
		D:    item.Item,
	}
}

// MakeServerSideFlagsOnlyPatchEvent creates a "patch" event for old server-side SDKs that use
// the flags-only stream.
func MakeServerSideFlagsOnlyPatchEvent(key string, item ldstoretypes.ItemDescriptor) eventsource.Event {
	return upsertEvent{
		Path: "/" + key,
		D:    item.Item,
	}
}

// MakeServerSideDeleteEvent creates a "delete" event for server-side SDKs.
func MakeServerSideDeleteEvent(kind ldstoretypes.DataKind, key string, version int) eventsource.Event {
	return deleteEvent{
		Path:    "/" + dataKindAPIName[kind] + "/" + key,
		Version: version,
	}
}

// MakeServerSideFlagsOnlyDeleteEvent creates a "delete" event for old server-side SDKs that use the
// flags-only stream.
func MakeServerSideFlagsOnlyDeleteEvent(key string, version int) eventsource.Event {
	return deleteEvent{
		Path:    "/" + key,
		Version: version,
	}
}

// MakePingEvent creates a "ping" event for client-side SDKs.
func MakePingEvent() eventsource.Event {
	return pingEvent{}
}

func (t flagsPutEvent) Id() string { //nolint:golint,stylecheck // nonstandard naming defined by eventsource interface
	return ""
}

func (t flagsPutEvent) Event() string {
	return "put"
}

func (t flagsPutEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t allPutEvent) Id() string { //nolint:golint,stylecheck // nonstandard naming defined by eventsource interface
	return ""
}

func (t allPutEvent) Event() string {
	return "put"
}

func (t allPutEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t upsertEvent) Id() string { //nolint:golint,stylecheck // nonstandard naming defined by eventsource interface
	return ""
}

func (t upsertEvent) Event() string {
	return "patch"
}

func (t upsertEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t deleteEvent) Id() string { //nolint:golint,stylecheck // nonstandard naming defined by eventsource interface
	return ""
}

func (t deleteEvent) Event() string {
	return "delete"
}

func (t deleteEvent) Data() string {
	data, _ := json.Marshal(t)

	return string(data)
}

func (t pingEvent) Id() string { //nolint:golint,stylecheck // nonstandard naming defined by eventsource interface
	return ""
}

func (t pingEvent) Event() string {
	return "ping"
}

func (t pingEvent) Data() string {
	return " " // We need something or the data field is not published by eventsource causing the event to be ignored
}
