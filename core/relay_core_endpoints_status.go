package core

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

type statusRep struct {
	Environments  map[string]environmentStatusRep `json:"environments"`
	Status        string                          `json:"status"`
	Version       string                          `json:"version"`
	ClientVersion string                          `json:"clientVersion"`
}

type environmentStatusRep struct {
	SDKKey    string `json:"sdkKey"`
	EnvID     string `json:"envId,omitempty"`
	MobileKey string `json:"mobileKey,omitempty"`
	Status    string `json:"status"`
}

var hexdigit = regexp.MustCompile(`[a-fA-F\d]`) //nolint:gochecknoglobals

func obscureKey(key string) string {
	if len(key) > 8 {
		return key[0:4] + hexdigit.ReplaceAllString(key[4:len(key)-5], "*") + key[len(key)-5:]
	}
	return key
}

func statusHandler(core *RelayCore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := statusRep{
			Environments:  make(map[string]environmentStatusRep),
			Version:       core.Version,
			ClientVersion: ld.Version,
		}

		healthy := true
		for _, clientCtx := range core.GetAllEnvironments() {
			var status environmentStatusRep

			for _, c := range clientCtx.GetCredentials() {
				switch c := c.(type) {
				case config.SDKKey:
					status.SDKKey = obscureKey(string(c))
				case config.MobileKey:
					status.MobileKey = obscureKey(string(c))
				case config.EnvironmentID:
					status.EnvID = string(c)
				}
			}

			client := clientCtx.GetClient()
			if client == nil || !client.Initialized() {
				status.Status = "disconnected"
				healthy = false
			} else {
				status.Status = "connected"
			}
			resp.Environments[clientCtx.GetName()] = status
		}

		if healthy {
			resp.Status = "healthy"
		} else {
			resp.Status = "degraded"
		}

		data, _ := json.Marshal(resp)

		_, _ = w.Write(data)
	})
}
