package autoconfig

import "github.com/launchdarkly/ld-relay/v6/config"

// These SSE message types are exported so that tests in other packages can more easily create
// simulated auto-config data. They should not be used by non-test code in other packages.

const (
	// PutEvent is the SSE event name corresponding to PutMessageData.
	PutEvent = "put"

	// PatchEvent is the SSE event name corresponding to PatchMessageData.
	PatchEvent = "patch"

	// DeleteEvent is the SSE event name corresponding to DeleteMessageData.
	DeleteEvent = "delete"

	// ReconnectEvent is the SSE event name for a message that forces a stream reconnect.
	ReconnectEvent = "reconnect"

	environmentPathPrefix = "/environments/"
)

// PutMessageData is the JSON data for an SSE message that provides a full set of environments.
type PutMessageData struct {
	// Path is currently always "/" for this message type.
	Path string `json:"path"`

	// Data contains the environment map.
	Data PutContent `json:"data"`
}

// PatchMessageData is the JSON data for an SSE message that adds or updates a single environment.
type PatchMessageData struct {
	// Path is currently always "environments/$ENVID".
	Path string `json:"path"`

	// Data is the environment representation.
	Data EnvironmentRep `json:"data"`
}

// DeleteMessageData is the JSON data for an SSE message that removes an environment.
type DeleteMessageData struct {
	// Path is currently always "environments/$ENVID".
	Path string `json:"path"`

	// Version is a number that must be greater than the last known version of the environment.
	Version int `json:"version"`
}

// PutContent is the environent map within PutMessageData.
type PutContent struct {
	// Environments is a map of environment representations.
	Environments map[config.EnvironmentID]EnvironmentRep `json:"environments"`
}
