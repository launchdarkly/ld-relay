package core

import (
	"encoding/json"
	"net/http"
	"regexp"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

const (
	statusEnvConnected    = "connected"
	statusEnvDisconnected = "disconnected"
	statusRelayHealthy    = "healthy"
	statusRelayDegraded   = "degraded"
)

type statusRep struct {
	Environments  map[string]environmentStatusRep `json:"environments"`
	Status        string                          `json:"status"`
	Version       string                          `json:"version"`
	ClientVersion string                          `json:"clientVersion"`
}

type environmentStatusRep struct {
	SDKKey           string              `json:"sdkKey"`
	EnvID            string              `json:"envId,omitempty"`
	EnvKey           string              `json:"envKey,omitempty"`
	EnvName          string              `json:"envName,omitempty"`
	ProjKey          string              `json:"projKey,omitempty"`
	ProjName         string              `json:"projName,omitempty"`
	MobileKey        string              `json:"mobileKey,omitempty"`
	ExpiringSDKKey   string              `json:"expiringSdkKey,omitempty"`
	Status           string              `json:"status"`
	ConnectionStatus connectionStatusRep `json:"connectionStatus"`
	DataStoreStatus  *dataStoreStatusRep `json:"dataStoreStatus,omitempty"`
}

type connectionStatusRep struct {
	State      interfaces.DataSourceState `json:"state"`
	StateSince ldtime.UnixMillisecondTime `json:"stateSince"`
	LastError  *connectionErrorRep        `json:"lastError,omitempty"`
}

type connectionErrorRep struct {
	Kind interfaces.DataSourceErrorKind `json:"kind"`
	Time ldtime.UnixMillisecondTime     `json:"time"`
}

type dataStoreStatusRep struct {
	State      string                     `json:"state"`
	StateSince ldtime.UnixMillisecondTime `json:"stateSince"`
}

var (
	hexDigitRegex    = regexp.MustCompile(`[a-fA-F\d]`)        //nolint:gochecknoglobals
	alphaPrefixRegex = regexp.MustCompile(`^[a-z][a-z][a-z]-`) //nolint:gochecknoglobals
)

// ObscureKey returns an obfuscated version of an SDK key or mobile key.
func ObscureKey(key string) string {
	if alphaPrefixRegex.MatchString(key) {
		return key[0:4] + ObscureKey(key[4:])
	}
	if len(key) > 4 {
		return hexDigitRegex.ReplaceAllString(key[:len(key)-5], "*") + key[len(key)-5:]
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
					status.SDKKey = ObscureKey(string(c))
				case config.MobileKey:
					status.MobileKey = ObscureKey(string(c))
				case config.EnvironmentID:
					status.EnvID = string(c)
				}
			}

			client := clientCtx.GetClient()
			if client == nil {
				status.Status = statusEnvDisconnected
				status.ConnectionStatus.State = interfaces.DataSourceStateInitializing
				status.ConnectionStatus.StateSince = ldtime.UnixMillisFromTime(clientCtx.GetCreationTime())
				healthy = false
			} else {
				if client.Initialized() {
					status.Status = statusEnvConnected
				} else {
					status.Status = statusEnvDisconnected
					healthy = false
				}
				sourceStatus := client.GetDataSourceStatus()
				status.ConnectionStatus = connectionStatusRep{
					State:      sourceStatus.State,
					StateSince: ldtime.UnixMillisFromTime(sourceStatus.StateSince),
				}
				if sourceStatus.LastError.Kind != "" {
					status.ConnectionStatus.LastError = &connectionErrorRep{
						Kind: sourceStatus.LastError.Kind,
						Time: ldtime.UnixMillisFromTime(sourceStatus.LastError.Time),
					}
				}
				storeStatus := client.GetDataStoreStatus()
				status.DataStoreStatus = &dataStoreStatusRep{
					State:      "VALID",
					StateSince: ldtime.UnixMillisFromTime(storeStatus.LastUpdated),
				}
				if !storeStatus.Available {
					status.DataStoreStatus.State = "INTERRUPTED"
				}
			}

			statusKey := clientCtx.GetName()
			if core.envLogNameMode == relayenv.LogNameIsEnvID {
				// If we're identifying environments by environment ID in the log (which we do if there's any
				// chance that the environment name could change) then we should also identify them that way here.
				statusKey = status.EnvID
			}
			resp.Environments[statusKey] = status
		}

		if healthy {
			resp.Status = statusRelayHealthy
		} else {
			resp.Status = statusRelayDegraded
		}

		data, _ := json.Marshal(resp)

		_, _ = w.Write(data)
	})
}
