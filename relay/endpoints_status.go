package relay

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/api"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"

	"github.com/gorilla/mux"
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
			status, envHealthy := relay.buildEnvironmentStatus(clientCtx)
			if !envHealthy {
				healthy = false
			}

			identifiers := clientCtx.GetIdentifiers()
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

// singleEnvironmentStatusHandler handles requests for the status of a single environment or filter.
// Supports multiple route patterns:
// - /status/{identifier}
// - /status/{identifier}/filters/{filterKey}
// - /status/{projKey}/{envKey}
// - /status/{projKey}/{envKey}/filters/{filterKey}
func singleEnvironmentStatusHandler(relay *Relay) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract identifier from URL path
		vars := mux.Vars(req)

		// Check if this is a projKey/envKey route or a single identifier route
		var identifier string
		projKey := vars["projKey"]
		envKey := vars["envKey"]
		if projKey != "" && envKey != "" {
			// Route: /status/{projKey}/{envKey}[/filters/{filterKey}]
			identifier = projKey + "/" + envKey
		} else {
			// Route: /status/{identifier}[/filters/{filterKey}]
			identifier = vars["identifier"]
		}

		filterKey := config.FilterKey(vars["filterKey"]) // empty string if not present

		// Look up the environment
		env, err := relay.getEnvironmentByIdentifier(identifier, filterKey)
		if err != nil {
			// Determine appropriate status code
			statusCode := http.StatusNotFound
			if err == errRelayNotReady {
				statusCode = http.StatusServiceUnavailable
			}

			// Return error response
			w.WriteHeader(statusCode)
			errorResp := map[string]string{
				"error": err.Error(),
			}
			data, _ := json.Marshal(errorResp)
			_, _ = w.Write(data)
			return
		}

		// Build and return status for the single environment
		status, _ := relay.buildEnvironmentStatus(env)
		data, _ := json.Marshal(status)
		_, _ = w.Write(data)
	})
}

// buildEnvironmentStatus constructs an EnvironmentStatusRep for a single environment.
// Returns the status and a boolean indicating whether the environment is healthy.
func (r *Relay) buildEnvironmentStatus(clientCtx relayenv.EnvContext) (api.EnvironmentStatusRep, bool) {
	identifiers := clientCtx.GetIdentifiers()

	status := api.EnvironmentStatusRep{
		EnvKey:   identifiers.EnvKey, // these will only be non-empty if we're in auto-configured mode
		EnvName:  identifiers.EnvName,
		ProjKey:  identifiers.ProjKey,
		ProjName: identifiers.ProjName,
	}

	for _, c := range clientCtx.GetCredentials() {
		switch c := c.(type) {
		case config.SDKKey:
			status.SDKKey = sdks.ObscureKey(string(c))
		case config.MobileKey:
			status.MobileKey = sdks.ObscureKey(string(c))
		case config.EnvironmentID:
			status.EnvID = string(c)
		}
	}

	for _, c := range clientCtx.GetDeprecatedCredentials() {
		if key, ok := c.(config.SDKKey); ok {
			status.ExpiringSDKKey = sdks.ObscureKey(string(key))
		}
	}

	healthy := true
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
				r.config.Main.DisconnectedStatusTime.GetOrElse(config.DefaultDisconnectedStatusTime) {
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
			stalenessThreshold := r.config.Main.BigSegmentsStaleThreshold.GetOrElse(config.DefaultBigSegmentsStaleThreshold)
			if !synchronizedOn.IsDefined() || now > (synchronizedOn+ldtime.UnixMillisecondTime(stalenessThreshold.Milliseconds())) { //nolint: gosec
				bigSegmentStatus.PotentiallyStale = true
				if r.config.Main.BigSegmentsStaleAsDegraded {
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

	return status, healthy
}
