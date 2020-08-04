package autoconfig

import "github.com/launchdarkly/ld-relay/v6/core/config"

const (
	putEvent              = "put"
	patchEvent            = "patch"
	deleteEvent           = "delete"
	reconnectEvent        = "reconnect"
	environmentPathPrefix = "/environments/"
)

type autoConfigPutMessage struct {
	Path string            `json:"path"` // currently always "/"
	Data autoConfigPutData `json:"data"`
}

type autoConfigPatchMessage struct {
	Path string         `json:"path"` // currently always "environments/$ENVID"
	Data environmentRep `json:"data"`
}

type autoConfigDeleteMessage struct {
	Path    string `json:"path"` // currently always "environments/$ENVID"
	Version int    `json:"version"`
}

type autoConfigPutData struct {
	Environments map[config.EnvironmentID]environmentRep `json:"environments"`
}
