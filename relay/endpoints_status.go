package relay

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/api"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
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

			// Scalar fields: anchor SDK key and primary mobile key.
			if key := clientCtx.GetSDKKey(); key.Defined() {
				status.SDKKey = sdks.ObscureKey(string(key))
			}
			if key := clientCtx.GetMobileKey(); key.Defined() {
				status.MobileKey = sdks.ObscureKey(string(key))
			}
			for _, c := range clientCtx.GetCredentials() {
				if envID, ok := c.(config.EnvironmentID); ok {
					status.EnvID = string(envID)
				}
			}

			// sdkKeys[]: non-anchor accepted SDK keys, sorted by identifier.
			status.SDKKeys = buildKeyStatusSlice(clientCtx.GetAcceptedSDKKeys())

			// mobileKeys[]: non-primary accepted mobile keys, sorted by identifier.
			status.MobileKeys = buildMobileKeyStatusSlice(clientCtx.GetAcceptedMobileKeys())

			// expiringSdkKey: pick the non-anchor SDK key with the soonest expiry for a deterministic
			// value; with multiple expiring keys the previous loop-overwrite was nondeterministic.
			if key, ok := clientCtx.GetEarliestExpiringSDKKey(); ok {
				status.ExpiringSDKKey = sdks.ObscureKey(string(key))
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

// buildKeyStatusSlice converts SDK key entries from the rotator into the JSON-serialisable slice for
// the sdkKeys[] response field. The entries are pre-sorted by the rotator.
func buildKeyStatusSlice(entries []credential.SDKKeyEntry) []api.KeyStatus {
	result := make([]api.KeyStatus, 0, len(entries))
	for _, e := range entries {
		ks := api.KeyStatus{
			Key:   e.Identifier,
			Value: sdks.ObscureKey(string(e.Value)),
		}
		if e.Expiry != nil {
			ms := e.Expiry.UnixMilli()
			ks.Expiry = &ms
		}
		result = append(result, ks)
	}
	return result
}

// buildMobileKeyStatusSlice converts mobile key entries into the JSON-serialisable slice for the
// mobileKeys[] response field. The entries are pre-sorted by the rotator.
func buildMobileKeyStatusSlice(entries []credential.MobileKeyEntry) []api.KeyStatus {
	result := make([]api.KeyStatus, 0, len(entries))
	for _, e := range entries {
		ks := api.KeyStatus{
			Key:   e.Identifier,
			Value: sdks.ObscureKey(string(e.Value)),
		}
		if e.Expiry != nil {
			ms := e.Expiry.UnixMilli()
			ks.Expiry = &ms
		}
		result = append(result, ks)
	}
	return result
}
