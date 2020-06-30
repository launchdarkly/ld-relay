package ldservices

import "encoding/json"

// KeyedData is an interface for use with ServerSideData as an abstraction for data model objects that
// have a key, since this package cannot depend on LaunchDarkly data model types themselves. The actual
// FeatureFlag and Segment types implement this method; you can also use FlagOrSegment for a stub object.
type KeyedData interface {
	GetKey() string
}

type fakeFlagOrSegment struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
}

func (f fakeFlagOrSegment) GetKey() string {
	return f.Key
}

// FlagOrSegment provides a stub implementation of KeyedData that has only "key" and "version" properties.
// This may be enough for some testing purposes that don't require full flag or segment data.
func FlagOrSegment(key string, version int) KeyedData {
	return fakeFlagOrSegment{Key: key, Version: version}
}

// ServerSDKData is a convenience type for constructing a test server-side SDK data payload for PollingServiceHandler
// or StreamingServiceHandler. Its String() method returns a JSON object with the expected "flags" and "segments"
// properties.
//
//     data := NewServerSDKData().Flags(flag1, flag2)
//     handler := PollingServiceHandler(data)
//
// It also implements the eventsource.Event interface, simulating a "put" event for the streaming service.
type ServerSDKData struct {
	FlagsMap    map[string]interface{} `json:"flags"`
	SegmentsMap map[string]interface{} `json:"segments"`
}

// NewServerSDKData creates a ServerSDKData instance.
func NewServerSDKData() *ServerSDKData {
	return &ServerSDKData{make(map[string]interface{}), make(map[string]interface{})}
}

// String returns the JSON encoding of the struct as a string.
func (s *ServerSDKData) String() string {
	bytes, _ := json.Marshal(*s)
	return string(bytes)
}

// Flags adds the specified items to the struct's "flags" map.
//
// Each item may be either a stub object from FlagOrSegment or a real data model object that implements KeyedData.
func (s *ServerSDKData) Flags(flags ...KeyedData) *ServerSDKData {
	for _, flag := range flags {
		s.FlagsMap[flag.GetKey()] = flag
	}
	return s
}

// Segments adds the specified items to the struct's "segments" map.
//
// Each item may be either a stub object from FlagOrSegment or a real data model object that implements KeyedData.
func (s *ServerSDKData) Segments(segments ...KeyedData) *ServerSDKData {
	for _, segment := range segments {
		s.SegmentsMap[segment.GetKey()] = segment
	}
	return s
}

// Id is for the eventsource.Event interface.
func (s *ServerSDKData) Id() string { //nolint // standard capitalization would be ID(), but we didn't define this interface
	return ""
}

// Event is for the eventsource.Event interface. It returns "put".
func (s *ServerSDKData) Event() string {
	return "put"
}

// Data is for the eventsource.Event interface. It provides the marshalled data in the format used by the streaming
// service.
func (s *ServerSDKData) Data() string {
	return `{"path": "/", "data": ` + s.String() + "}"
}
