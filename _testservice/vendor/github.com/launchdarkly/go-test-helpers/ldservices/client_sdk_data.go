package ldservices

import (
	"encoding/json"

	"gopkg.in/launchdarkly/go-sdk-common.v1/ldvalue"
)

// FlagValueData corresponds to the representation used by the client-side SDK endpoints.
//
// It also implements the eventsource.Event interface, simulating a "patch" event for the streaming service.
type FlagValueData struct {
	// Key is the flag key.
	Key string
	// Version is the flag version, as defined in the server-side SDK endpoints.
	Version int
	// FlagVersion is another version property that is provided only by client-side endpoints.
	FlagVersion int
	// Value is the variation value for the current user.
	Value ldvalue.Value
	// VariationIndex is the variation index for the current user. Use -1 to omit this.
	VariationIndex int
	// Reason is the evaluation reason, if available. This is specified as an `ldvalue.Value` to avoid having to
	// bring in `EvaluationReason` and related types; the caller is responsible for providing the JSON
	// representation of the reason.
	Reason ldvalue.Value
	// TrackEvents is true if full event tracking is enabled for this flag.
	TrackEvents bool
	// DebugEventsUntilDate is the time, if any, until which debugging is enabled for this flag.
	DebugEventsUntilDate uint64
}

// Id is for the eventsource.Event interface.
func (f FlagValueData) Id() string { //nolint // standard capitalization would be ID(), but we didn't define this interface
	return ""
}

// Event is for the eventsource.Event interface. It returns "patch".
func (f FlagValueData) Event() string {
	return "patch"
}

// Data is for the eventsource.Event interface. It provides the marshalled data in the format used by the streaming
// service.
func (f FlagValueData) Data() string {
	return string(f.ToJSON(true))
}

// ToJSON returns the JSON representation of the flag data.
func (f FlagValueData) ToJSON(withKey bool) []byte {
	d := flagValueDataJSON{
		Version:     f.Version,
		FlagVersion: f.FlagVersion,
		Value:       f.Value,
	}
	if withKey {
		d.Key = &f.Key
	}
	if f.VariationIndex >= 0 {
		d.VariationIndex = &f.VariationIndex
	}
	if !f.Reason.IsNull() {
		d.Reason = &f.Reason
	}
	if f.TrackEvents {
		d.TrackEvents = &f.TrackEvents
	}
	if f.DebugEventsUntilDate > 0 {
		d.DebugEventsUntilDate = &f.DebugEventsUntilDate
	}
	json, _ := json.Marshal(d)
	return json
}

type flagValueDataJSON struct {
	Key                  *string        `json:"key,omitempty"`
	Version              int            `json:"version"`
	FlagVersion          int            `json:"flagVersion"`
	Value                ldvalue.Value  `json:"value"`
	VariationIndex       *int           `json:"variation,omitempty"`
	Reason               *ldvalue.Value `json:"reason,omitempty"`
	TrackEvents          *bool          `json:"trackEvents,omitempty"`
	DebugEventsUntilDate *uint64        `json:"debugEventsUntilDate,omitempty"`
}

// ClientSDKData is a set of flag value data as provided by the client-side SDK endpoints.
//
// It also implements the eventsource.Event interface, simulating a "put" event for the streaming service.
type ClientSDKData map[string]json.RawMessage

// NewClientSDKData creates a ClientSDKData instance.
//
// This constructor is provided in case we ever change the implementation to be something other than just a map.
func NewClientSDKData() ClientSDKData {
	return make(ClientSDKData)
}

// Flags adds the specified items to the flags map.
func (c ClientSDKData) Flags(flags ...FlagValueData) ClientSDKData {
	for _, flag := range flags {
		c[flag.Key] = flag.ToJSON(false)
	}
	return c
}

// String returns the JSON encoding of the struct as a string.
func (c ClientSDKData) String() string {
	bytes, _ := json.Marshal(c)
	return string(bytes)
}

// Id is for the eventsource.Event interface.
func (c ClientSDKData) Id() string { //nolint // standard capitalization would be ID(), but we didn't define this interface
	return ""
}

// Event is for the eventsource.Event interface. It returns "put".
func (c ClientSDKData) Event() string {
	return "put"
}

// Data is for the eventsource.Event interface. It provides the marshalled data in the format used by the streaming
// service.
func (c ClientSDKData) Data() string {
	return c.String()
}
