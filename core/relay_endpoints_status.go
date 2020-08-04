package core

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/launchdarkly/ld-relay/v6/internal/version"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

type environmentStatus struct {
	SdkKey    string `json:"sdkKey"`
	EnvId     string `json:"envId,omitempty"`
	MobileKey string `json:"mobileKey,omitempty"`
	Status    string `json:"status"`
}

var hexdigit = regexp.MustCompile(`[a-fA-F\d]`)

func obscureKey(key string) string {
	if len(key) > 8 {
		return key[0:4] + hexdigit.ReplaceAllString(key[4:len(key)-5], "*") + key[len(key)-5:]
	}
	return key
}

func statusHandler(core *RelayCore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		envs := make(map[string]environmentStatus)

		healthy := true
		for _, clientCtx := range core.GetAllEnvironments() {
			var status environmentStatus
			creds := clientCtx.GetCredentials()
			status.SdkKey = obscureKey(string(creds.SDKKey))
			if creds.MobileKey != "" {
				status.MobileKey = obscureKey(string(creds.MobileKey))
			}
			status.EnvId = string(creds.EnvironmentID)
			client := clientCtx.GetClient()
			if client == nil || !client.Initialized() {
				status.Status = "disconnected"
				healthy = false
			} else {
				status.Status = "connected"
			}
			envs[clientCtx.GetName()] = status
		}

		resp := struct {
			Environments  map[string]environmentStatus `json:"environments"`
			Status        string                       `json:"status"`
			Version       string                       `json:"version"`
			ClientVersion string                       `json:"clientVersion"`
		}{
			Environments:  envs,
			Version:       version.Version,
			ClientVersion: ld.Version,
		}

		if healthy {
			resp.Status = "healthy"
		} else {
			resp.Status = "degraded"
		}

		data, _ := json.Marshal(resp)

		w.Write(data)
	})
}
