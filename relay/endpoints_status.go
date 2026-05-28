package relay

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/api"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
)

const (
	statusEnvConnected    = "connected"
	statusEnvDisconnected = "disconnected"
	statusRelayHealthy    = "healthy"
	statusRelayDegraded   = "degraded"
)

func statusHandler(relay *Relay) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := api.StatusRep{
			Environments:  make(map[string]api.EnvironmentStatusRep),
			Version:       relay.version,
			ClientVersion: ld.Version,
		}

		relay.lock.Lock()
		fullyConfigured := relay.fullyConfigured
		relay.lock.Unlock()

		healthy := fullyConfigured
		for _, clientCtx := range relay.getAllEnvironments() {
			identifiers := clientCtx.GetIdentifiers()

			status := api.EnvironmentStatusRep{
				EnvKey:   identifiers.EnvKey, // these will only be non-empty if we're in auto-configured mode
				EnvName:  identifiers.EnvName,
				ProjKey:  identifiers.ProjKey,
				ProjName: identifiers.ProjName,
			}

			primarySDKKey := clientCtx.PrimarySDKKey()
			primaryMobileKey := clientCtx.PrimaryMobileKey()
			for _, c := range clientCtx.GetCredentials() {
				switch c := c.(type) {
				case config.SDKKey:
					obscured := sdks.ObscureKey(string(c))
					if c == primarySDKKey {
						status.SDKKey = obscured
					} else {
						status.AdditionalSDKKeys = append(status.AdditionalSDKKeys, obscured)
					}
				case config.MobileKey:
					obscured := sdks.ObscureKey(string(c))
					if c == primaryMobileKey {
						status.MobileKey = obscured
					} else {
						status.AdditionalMobileKeys = append(status.AdditionalMobileKeys, obscured)
					}
				case config.EnvironmentID:
					status.EnvID = string(c)
				}
			}

			// Surface every deprecated credential the rotator knows about, partitioned by kind.
			// The scalar ExpiringSDKKey / ExpiringMobileKey fields are retained for back-compat
			// with existing dashboards and carry the first key of each kind seen; the array
			// fields carry the full set so callers can inspect every grace-period entry (which is
			// possible now that additional keys can each carry their own ExpiresAt).
			for _, c := range clientCtx.GetDeprecatedCredentials() {
				switch key := c.(type) {
				case config.SDKKey:
					obscured := sdks.ObscureKey(string(key))
					if status.ExpiringSDKKey == "" {
						status.ExpiringSDKKey = obscured
					}
					status.ExpiringSDKKeys = append(status.ExpiringSDKKeys, obscured)
				case config.MobileKey:
					obscured := sdks.ObscureKey(string(key))
					if status.ExpiringMobileKey == "" {
						status.ExpiringMobileKey = obscured
					}
					status.ExpiringMobileKeys = append(status.ExpiringMobileKeys, obscured)
				}
			}

			client := clientCtx.GetClient()
			if client == nil {
				status.Status = statusEnvDisconnected
				status.ConnectionStatus.State = interfaces.DataSourceStateInitializing
				status.ConnectionStatus.StateSince = ldtime.UnixMillisFromTime(clientCtx.GetCreationTime())
				status.DataStoreStatus.State = "INITIALIZING"
				healthy = false
			} else {
				connected := client.Initialized()

				sourceStatus := client.GetDataSourceStatus()
				status.ConnectionStatus = api.ConnectionStatusRep{
					State:      sourceStatus.State,
					StateSince: ldtime.UnixMillisFromTime(sourceStatus.StateSince),
				}
				if sourceStatus.LastError.Kind != "" {
					status.ConnectionStatus.LastError = &api.ConnectionErrorRep{
						Kind: sourceStatus.LastError.Kind,
						Time: ldtime.UnixMillisFromTime(sourceStatus.LastError.Time),
					}
				}
				if sourceStatus.State != interfaces.DataSourceStateValid &&
					time.Since(sourceStatus.StateSince) >=
						relay.config.Main.DisconnectedStatusTime.GetOrElse(config.DefaultDisconnectedStatusTime) {
					connected = false
				}

				storeStatus := client.GetDataStoreStatus()
				status.DataStoreStatus.State = "VALID"
				status.DataStoreStatus.StateSince = ldtime.UnixMillisFromTime(storeStatus.LastUpdated)
				if !storeStatus.Available {
					status.DataStoreStatus.State = "INTERRUPTED"
				}

				if connected {
					status.Status = statusEnvConnected
				} else {
					status.Status = statusEnvDisconnected
					healthy = false
				}
			}

			bigSegmentStore := clientCtx.GetBigSegmentStore()
			if bigSegmentStore != nil {
				bigSegmentStatus := api.BigSegmentStatusRep{}
				synchronizedOn, err := bigSegmentStore.GetSynchronizedOn()
				if err != nil {
					bigSegmentStatus.Available = false
				} else {
					bigSegmentStatus.Available = true
					bigSegmentStatus.LastSynchronizedOn = synchronizedOn
					now := ldtime.UnixMillisNow()
					stalenessThreshold := relay.config.Main.BigSegmentsStaleThreshold.GetOrElse(config.DefaultBigSegmentsStaleThreshold)
					if !synchronizedOn.IsDefined() || now > (synchronizedOn+ldtime.UnixMillisecondTime(stalenessThreshold.Milliseconds())) { //nolint: gosec
						bigSegmentStatus.PotentiallyStale = true
						if relay.config.Main.BigSegmentsStaleAsDegraded {
							healthy = false
						}
					}
				}
				status.BigSegmentStatus = &bigSegmentStatus
			}

			storeInfo := clientCtx.GetDataStoreInfo()
			status.DataStoreStatus.Database = storeInfo.DBType
			status.DataStoreStatus.DBServer = storeInfo.DBServer
			status.DataStoreStatus.DBPrefix = storeInfo.DBPrefix
			status.DataStoreStatus.DBTable = storeInfo.DBTable

			statusKey := identifiers.GetDisplayName()
			if relay.envLogNameMode == relayenv.LogNameIsEnvID {
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
