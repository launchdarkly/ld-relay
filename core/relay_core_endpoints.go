package core

import (
	"crypto/sha1" //nolint:gosec // we're not using SHA1 for encryption, just for generating an insecure hash
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/launchdarkly/ld-relay/v6/core/internal/events"
	"github.com/launchdarkly/ld-relay/v6/core/internal/util"
	"github.com/launchdarkly/ld-relay/v6/core/logging"
	"github.com/launchdarkly/ld-relay/v6/core/middleware"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/streams"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldeval "gopkg.in/launchdarkly/go-server-sdk-evaluation.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"

	"github.com/gorilla/mux"
)

type evalXResult struct {
	Value                ldvalue.Value               `json:"value"`
	Variation            *int                        `json:"variation,omitempty"`
	Version              int                         `json:"version"`
	DebugEventsUntilDate *ldtime.UnixMillisecondTime `json:"debugEventsUntilDate,omitempty"`
	TrackEvents          bool                        `json:"trackEvents,omitempty"`
	TrackReason          bool                        `json:"trackReason,omitempty"`
	Reason               *ldreason.EvaluationReason  `json:"reason,omitempty"`
}

func getClientSideUserProperties(
	clientCtx relayenv.EnvContext,
	sdkKind sdks.Kind,
	req *http.Request,
	w http.ResponseWriter,
) (lduser.User, bool) {
	var user lduser.User
	var userDecodeErr error

	if req.Method == "REPORT" {
		if req.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte("Content-Type must be application/json."))
			return user, false
		}
		body, _ := ioutil.ReadAll(req.Body)
		userDecodeErr = json.Unmarshal(body, &user)
	} else {
		base64User := mux.Vars(req)["user"]
		user, userDecodeErr = middleware.UserFromBase64(base64User)
	}
	if userDecodeErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg(userDecodeErr.Error()))
		return user, false
	}

	if clientCtx.IsSecureMode() && sdkKind == sdks.JSClient {
		hash := req.URL.Query().Get("h")
		valid := false
		if hash != "" {
			validHash := clientCtx.GetClient().SecureModeHash(user)
			valid = hash == validHash
		}
		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(util.ErrorJSONMsg("Environment is in secure mode, and user hash does not match."))
			return user, false
		}
	}

	return user, true
}

// Old stream endpoint that just sends "ping" events: clientstream.ld.com/mping (mobile)
// or clientstream.ld.com/ping/{envId} (JS)
func pingStreamHandler(streamProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLoggers().Debug("Application requested client-side ping stream")
		clientCtx.Env.GetStreamHandler(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
	})
}

// This handler is used for client-side streaming endpoints that require user properties. Currently it is
// implemented the same as the ping stream once we have validated the user.
func pingStreamHandlerWithUser(sdkKind sdks.Kind, streamProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLoggers().Debug("Application requested client-side ping stream")

		if _, ok := getClientSideUserProperties(clientCtx.Env, sdkKind, req, w); ok {
			clientCtx.Env.GetStreamHandler(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
		}
	})
}

// Multi-purpose streaming handler; all details of the behavior of the particular type of stream are
// abstracted in StreamProvider and EnvStreams
func streamHandler(streamProvider streams.StreamProvider, logMessage string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLoggers().Debug(logMessage)
		clientCtx.Env.GetStreamHandler(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
	})
}

// PHP SDK polling endpoint for all flags: app.ld.com/sdk/flags
func pollAllFlagsHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	data, err := clientCtx.Env.GetStore().GetAll(ldstoreimpl.Features())
	if err != nil {
		clientCtx.Env.GetLoggers().Errorf("Error reading feature store: %s", err)
		w.WriteHeader(500)
		return
	}
	respData := itemsCollectionToMap(data)
	// Compute an overall Etag for the data set by hashing flag keys and versions
	hash := sha1.New()                                                         // nolint:gas // just used for insecure hashing
	sort.Slice(data, func(i, j int) bool { return data[i].Key < data[j].Key }) // makes the hash deterministic
	for _, item := range data {
		_, _ = io.WriteString(hash, fmt.Sprintf("%s:%d", item.Key, item.Item.Version))
	}
	etag := hex.EncodeToString(hash.Sum(nil))[:15]
	writeCacheableJSONResponse(w, req, clientCtx.Env, respData, etag)
}

// PHP SDK polling endpoint for a flag: app.ld.com/sdk/flags/{key}
func pollFlagHandler(w http.ResponseWriter, req *http.Request) {
	pollFlagOrSegment(middleware.GetEnvContextInfo(req.Context()).Env, ldstoreimpl.Features())(w, req)
}

// PHP SDK polling endpoint for a segment: app.ld.com/sdk/segments/{key}
func pollSegmentHandler(w http.ResponseWriter, req *http.Request) {
	pollFlagOrSegment(middleware.GetEnvContextInfo(req.Context()).Env, ldstoreimpl.Segments())(w, req)
}

// Event-recorder endpoints:
// events.ld.com/bulk (server-side)
// events.ld.com/diagnostic (server-side diagnostic)
// events.ld.com/mobile, events.ld.com/mobile/events, events.ld.com/mobileevents/bulk (mobile)
// events.ld.com/mobile/events/diagnostic (mobile diagnostic)
// events.ld.com/events/bulk/{envId} (JS)
// events.ld.com/events/diagnostic/{envId} (JS)
func bulkEventHandler(endpoint events.Endpoint) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		dispatcher := clientCtx.Env.GetEventDispatcher()
		if dispatcher == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(util.ErrorJSONMsg("Event proxy is not enabled for this environment"))
			return
		}
		handler := dispatcher.GetHandler(endpoint)
		if handler == nil {
			// Note, if this ever happens, it is a programming error since we are only supposed to
			// be using a fixed set of Endpoint values that the dispatcher knows about.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(util.ErrorJSONMsg("Internal error in event proxy"))
			logging.GetGlobalContextLoggers(req.Context()).Errorf("Tried to proxy events for unsupported endpoint '%s'", endpoint)
			return
		}
		handler(w, req)
	})
}

// Client-side evaluation endpoint, new schema with metadata:
// app.ld.com/sdk/evalx/{envId}/users/{user} (GET)
// app.ld.com/sdk/evalx/{envId}/user (REPORT)
// app.ld/com/sdk/evalx/users/{user} (GET - with SDK key auth)
// app.ld/com/sdk/evalx/user (REPORT - with SDK key auth)
func evaluateAllFeatureFlags(sdkKind sdks.Kind) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		evaluateAllShared(w, req, false, sdkKind)
	}
}

// Client-side evaluation endpoint, old schema with only values:
// app.ld.com/sdk/eval/{envId}/users/{user} (GET)
// app.ld.com/sdk/eval/{envId}/user (REPORT)
// app.ld/com/sdk/eval/users/{user} (GET - with SDK key auth)
// app.ld/com/sdk/eval/user (REPORT - with SDK key auth)
func evaluateAllFeatureFlagsValueOnly(sdkKind sdks.Kind) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		evaluateAllShared(w, req, true, sdkKind)
	}
}

func evaluateAllShared(w http.ResponseWriter, req *http.Request, valueOnly bool, sdkKind sdks.Kind) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	client := clientCtx.Env.GetClient()
	store := clientCtx.Env.GetStore()
	loggers := clientCtx.Env.GetLoggers()

	user, ok := getClientSideUserProperties(clientCtx.Env, sdkKind, req, w)
	if !ok {
		return
	}

	withReasons := req.URL.Query().Get("withReasons") == "true"

	w.Header().Set("Content-Type", "application/json")

	if !client.Initialized() {
		if store.IsInitialized() {
			loggers.Warn("Called before client initialization; using last known values from feature store")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			loggers.Warn("Called before client initialization. Feature store not available")
			_, _ = w.Write(util.ErrorJSONMsg("Service not initialized"))
			return
		}
	}

	if user.GetKey() == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg("User must have a 'key' attribute"))
		return
	}

	loggers.Debugf("Application requested client-side flags (%s) for user: %s", sdkKind, user.GetKey())

	items, err := store.GetAll(ldstoreimpl.Features())
	if err != nil {
		loggers.Warnf("Unable to fetch flags from feature store. Returning nil map. Error: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(util.ErrorJSONMsgf("Error fetching flags from feature store: %s", err))
		return
	}

	evaluator := ldeval.NewEvaluator(ldstoreimpl.NewDataStoreEvaluatorDataProvider(store, loggers))

	response := make(map[string]interface{}, len(items))
	for _, item := range items {
		if flag, ok := item.Item.Item.(*ldmodel.FeatureFlag); ok {
			switch sdkKind {
			case sdks.JSClient:
				if !flag.ClientSideAvailability.UsingEnvironmentID {
					continue
				}
			case sdks.Mobile:
				if !flag.ClientSideAvailability.UsingMobileKey {
					continue
				}
			}
			detail := evaluator.Evaluate(flag, user, nil)
			var result interface{}
			if valueOnly {
				result = detail.Value
			} else {
				isExperiment := flag.IsExperimentationEnabled(detail.Reason)
				value := evalXResult{
					Value:       detail.Value,
					Version:     flag.Version,
					TrackEvents: flag.TrackEvents || isExperiment,
					TrackReason: isExperiment,
				}
				if detail.VariationIndex >= 0 {
					value.Variation = &detail.VariationIndex
				}
				if withReasons || isExperiment {
					value.Reason = &detail.Reason
				}
				if flag.DebugEventsUntilDate != 0 {
					value.DebugEventsUntilDate = &flag.DebugEventsUntilDate
				}
				result = value
			}
			response[flag.Key] = result
		}
	}

	result, _ := json.Marshal(response)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func pollFlagOrSegment(clientContext relayenv.EnvContext, kind ldstoretypes.DataKind) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		key := mux.Vars(req)["key"]
		item, err := clientContext.GetStore().Get(kind, key)
		if err != nil {
			clientContext.GetLoggers().Errorf("Error reading feature store: %s", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if item.Item == nil {
			w.WriteHeader(http.StatusNotFound)
		} else {
			writeCacheableJSONResponse(w, req, clientContext, item.Item, strconv.Itoa(item.Version))
		}
	}
}

func writeCacheableJSONResponse(w http.ResponseWriter, req *http.Request, clientContext relayenv.EnvContext,
	entity interface{}, etagValue string) {
	etag := fmt.Sprintf("relay-%s", etagValue) // just to make it extra clear that these are relay-specific etags
	if cachedEtag := req.Header.Get("If-None-Match"); cachedEtag != "" {
		if cachedEtag == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	bytes, err := json.Marshal(entity)
	if err != nil {
		clientContext.GetLoggers().Errorf("Error marshaling JSON: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Etag", etag)
		ttl := clientContext.GetTTL()
		if ttl > 0 {
			w.Header().Set("Vary", "Authorization")
			expiresAt := time.Now().UTC().Add(ttl)
			w.Header().Set("Expires", expiresAt.Format(http.TimeFormat))
			// We're setting "Expires:" instead of "Cache-Control:max-age=" so that if someone puts an
			// HTTP cache in front of ld-relay, multiple clients hitting the cache at different times
			// will all see the same expiration time.
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes)
	}
}

func itemsCollectionToMap(coll []ldstoretypes.KeyedItemDescriptor) map[string]interface{} {
	ret := make(map[string]interface{}, len(coll))
	for _, item := range coll {
		ret[item.Key] = item.Item.Item
	}
	return ret
}
