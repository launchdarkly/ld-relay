package relay

import (
	"encoding/json"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/internal/version"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

type environmentStatus struct {
	SdkKey    string `json:"sdkKey"`
	EnvId     string `json:"envId,omitempty"`
	MobileKey string `json:"mobileKey,omitempty"`
	Status    string `json:"status"`
}

func statusHandler(core *RelayCore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		envs := make(map[string]environmentStatus)

		healthy := true
		for _, clientCtx := range core.GetAllEnvironments() {
			var status environmentStatus
			creds := clientCtx.GetCredentials()
			status.SdkKey = obscureKey(creds.SDKKey)
			if mobileKey, ok := creds.MobileKey.Get(); ok {
				status.MobileKey = obscureKey(mobileKey)
			}
			status.EnvId = creds.EnvironmentID.StringValue()
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
