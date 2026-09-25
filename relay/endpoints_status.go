package relay

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/api"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sdks"

	"github.com/gorilla/mux"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
)

func statusHandler(relay *Relay) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := api.StatusRep{
			Environments:  make(map[string]api.EnvironmentStatusRep),
			Version:       relay.version,
			ClientVersion: ld.Version,
		}

		envs, healthy := relay.collectEnvironmentStatuses()
		for _, env := range envs {
			resp.Environments[env.key] = env.rep
		}

		resp.AutoConfigStatus = relay.buildAutoConfigStatus()

		if healthy {
			resp.Status = api.StatusHealthy
		} else {
			resp.Status = api.StatusDegraded
		}

		data, _ := json.Marshal(resp)

		writeStatusBody(w, req, data, api.SchemaAllEnvironments)
	})
}

// writeStatusBody writes the status document, or -- when the caller supplied "expect" clauses -- the
// verdict for them: the per-clause results as the body, and an HTTP status code the caller can branch
// on without parsing the body themselves (200 satisfied, 412 unsatisfied, 422 not evaluable, 400
// unparseable). schema says which status document the clause paths are checked against.
func writeStatusBody(w http.ResponseWriter, req *http.Request, body []byte, schema api.StatusSchema) {
	clauses, requested, malformed := api.ParseExpectQuery(req.URL.RawQuery)
	switch {
	case !requested:
		_, _ = w.Write(body)
	case malformed:
		// At least one clause could not be decoded, so the relay does not know everything that was
		// asserted and must not answer 200 for an assertion it never evaluated. The whole query is
		// rejected rather than the clauses that survived being judged on their own.
		writeStatusVerdict(w, api.ExpectationsResult{Error: "could not parse the query string"},
			http.StatusBadRequest)
	default:
		result, code := api.EvaluateExpectations(body, clauses, schema)
		writeStatusVerdict(w, result, code)
	}
}

func writeStatusVerdict(w http.ResponseWriter, result api.ExpectationsResult, code int) {
	out, _ := json.Marshal(result)
	w.WriteHeader(code)
	_, _ = w.Write(out)
}

// singleEnvironmentStatusHandler handles requests for the status of a single environment or filter.
// Supports multiple route patterns:
// - /status/{identifier}
// - /status/{projKey}/{envKey}
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
			// Route: /status/{projKey}/{envKey}
			identifier = projKey + "/" + envKey
		} else {
			// Route: /status/{identifier}
			identifier = vars["identifier"]
		}

		// Look up the environment
		env, err := relay.getEnvironmentByIdentifier(identifier)
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

		writeStatusBody(w, req, data, api.SchemaSingleEnvironment)
	})
}

// environmentStatus is one environment's status document, with both of the names its consumers
// need. The status endpoint keys the document by key, which is the environment ID whenever the
// configured name could change. The metrics report name, which is the same display name that the
// request metrics carry, so that status series and traffic series can be joined.
type environmentStatus struct {
	key  string
	name string
	rep  api.EnvironmentStatusRep
}

// collectEnvironmentStatuses builds the status of every environment Relay serves, and rolls their
// health up into one verdict for the Relay as a whole. The verdict is healthy only if Relay knows
// its full set of environments and every one of them is healthy.
//
// An environment can be unhealthy while its own status still reads connected: stale big segments
// count against the Relay when bigSegmentsStaleAsDegraded is set.
func (r *Relay) collectEnvironmentStatuses() ([]environmentStatus, bool) {
	r.lock.Lock()
	fullyConfigured := r.fullyConfigured
	r.lock.Unlock()

	healthy := fullyConfigured
	allEnvs := r.getAllEnvironments()
	envs := make([]environmentStatus, 0, len(allEnvs))
	for _, clientCtx := range allEnvs {
		rep, envHealthy := r.buildEnvironmentStatus(clientCtx)
		if !envHealthy {
			healthy = false
		}

		name := clientCtx.GetIdentifiers().GetDisplayName()
		key := name
		if r.envLogNameMode == relayenv.LogNameIsEnvID {
			// If we're identifying environments by environment ID in the log (which we do if there's any
			// chance that the environment name could change) then we should also identify them that way here.
			key = rep.EnvID
		}
		envs = append(envs, environmentStatus{key: key, name: name, rep: rep})
	}
	return envs, healthy
}

// buildAutoConfigStatus constructs the auto-configuration stream status, or nil if Relay is not in
// automatic configuration mode. A non-VALID state means Relay is no longer learning about
// environment changes, even though the environments it already knows about keep serving flags.
func (r *Relay) buildAutoConfigStatus() *api.AutoConfigStatusRep {
	// autoConfigStream is assigned once, when the Relay is constructed, so this needs no lock.
	if r.autoConfigStream == nil {
		return nil
	}

	status := r.autoConfigStream.Status()
	rep := &api.AutoConfigStatusRep{
		State:      status.State,
		StateSince: ldtime.UnixMillisFromTime(status.StateSince),
	}
	if status.LastError.Kind != "" {
		rep.LastError = &api.ConnectionErrorRep{
			Kind:       status.LastError.Kind,
			StatusCode: status.LastError.StatusCode,
			Time:       ldtime.UnixMillisFromTime(status.LastError.Time),
		}
	}
	return rep
}

// keyStatus converts one accepted credential into its status representation.
func keyStatus(value string, info credential.AcceptedKey) api.KeyStatus {
	ks := api.KeyStatus{Value: sdks.ObscureKey(value)}
	if info.Key != nil {
		ks.Key = *info.Key
	}
	if info.Expiry != nil {
		ms := info.Expiry.UnixMilli()
		ks.Expiry = &ms
	}
	return ks
}

// buildEnvironmentStatus constructs an EnvironmentStatusRep for a single environment.
// Returns the status and a boolean indicating whether the environment is healthy.
//
// Note that this reads the environment's big segment store, which is the only I/O the status
// gathering performs.
func (r *Relay) buildEnvironmentStatus(clientCtx relayenv.EnvContext) (api.EnvironmentStatusRep, bool) {
	identifiers := clientCtx.GetIdentifiers()

	status := api.EnvironmentStatusRep{
		EnvKey:   identifiers.EnvKey, // these will only be non-empty if we're in auto-configured mode
		EnvName:  identifiers.EnvName,
		ProjKey:  identifiers.ProjKey,
		ProjName: identifiers.ProjName,
	}

	// One snapshot drives every credential field, so they cannot disagree with each other under a
	// concurrent reconcile. Iterating GetCredentials for these would also be nondeterministic now that
	// an environment accepts a set of SDK and mobile keys rather than one of each.
	accepted := clientCtx.GetAcceptedKeys()
	if accepted.Anchor.Defined() {
		status.SDKKey = sdks.ObscureKey(string(accepted.Anchor))
	}
	if accepted.PrimaryMobile.Defined() {
		status.MobileKey = sdks.ObscureKey(string(accepted.PrimaryMobile))
	}
	for _, c := range clientCtx.GetCredentials() {
		if envID, ok := c.(config.EnvironmentID); ok {
			status.EnvID = string(envID)
		}
	}
	// The arrays are always present, never null: an environment with no mobile key reports an empty
	// mobileKeys rather than omitting it.
	status.SDKKeys = make([]api.KeyStatus, 0, len(accepted.Server))
	for value, info := range accepted.Server {
		status.SDKKeys = append(status.SDKKeys, keyStatus(string(value), info))
	}
	status.MobileKeys = make([]api.KeyStatus, 0, len(accepted.Mobile))
	for value, info := range accepted.Mobile {
		status.MobileKeys = append(status.MobileKeys, keyStatus(string(value), info))
	}

	healthy := true
	client := clientCtx.GetClient()
	if client == nil {
		status.Status = api.EnvStatusDisconnected
		status.ConnectionStatus.State = interfaces.DataSourceStateInitializing
		status.ConnectionStatus.StateSince = ldtime.UnixMillisFromTime(clientCtx.GetCreationTime())
		status.DataStoreStatus.State = api.DataStoreStateInitializing
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
				Kind:       sourceStatus.LastError.Kind,
				StatusCode: sourceStatus.LastError.StatusCode,
				Time:       ldtime.UnixMillisFromTime(sourceStatus.LastError.Time),
			}
		}
		if sourceStatus.State != interfaces.DataSourceStateValid &&
			time.Since(sourceStatus.StateSince) >=
				r.config.Main.DisconnectedStatusTime.GetOrElse(config.DefaultDisconnectedStatusTime) {
			connected = false
		}

		storeStatus := client.GetDataStoreStatus()
		status.DataStoreStatus.State = api.DataStoreStateValid
		status.DataStoreStatus.StateSince = ldtime.UnixMillisFromTime(storeStatus.LastUpdated)
		if !storeStatus.Available {
			status.DataStoreStatus.State = api.DataStoreStateInterrupted
		}

		if connected {
			status.Status = api.EnvStatusConnected
		} else {
			status.Status = api.EnvStatusDisconnected
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
