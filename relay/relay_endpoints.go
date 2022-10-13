package relay

import (
	"crypto/sha1" //nolint:gosec // we're not using SHA1 for encryption, just for generating an insecure hash
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/middleware"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/streams"
	"github.com/launchdarkly/ld-relay/v6/internal/util"

	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	ldevents "github.com/launchdarkly/go-sdk-events/v2"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"

	"github.com/gorilla/mux"
)

func getClientSideContextProperties(
	clientCtx relayenv.EnvContext,
	sdkKind basictypes.SDKKind,
	req *http.Request,
	w http.ResponseWriter,
) (ldcontext.Context, bool) {
	var ldContext ldcontext.Context
	var contextDecodeErr error

	if req.Method == "REPORT" {
		if req.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte("Content-Type must be application/json."))
			return ldContext, false
		}
		body, _ := io.ReadAll(req.Body)
		contextDecodeErr = json.Unmarshal(body, &ldContext)
	} else {
		base64Context := mux.Vars(req)["context"] // this assumes we have used {context} as a placeholder in the route
		ldContext, contextDecodeErr = middleware.ContextFromBase64(base64Context)
	}
	if contextDecodeErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg(contextDecodeErr.Error()))
		return ldContext, false
	}

	if clientCtx.IsSecureMode() && sdkKind == basictypes.JSClientSDK {
		hash := req.URL.Query().Get("h")
		valid := false
		if hash != "" {
			validHash := clientCtx.GetClient().SecureModeHash(ldContext)
			valid = hash == validHash
		}
		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(util.ErrorJSONMsg("Environment is in secure mode, and context hash does not match."))
			return ldContext, false
		}
	}

	return ldContext, true
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

// This handler is used for client-side streaming endpoints that require context properties. Currently it is
// implemented the same as the ping stream once we have validated the context.
func pingStreamHandlerWithContext(sdkKind basictypes.SDKKind, streamProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLoggers().Debug("Application requested client-side ping stream")

		if _, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, req, w); ok {
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
	respData := serializeFlagsAsMap(data)
	// Compute an overall Etag for the data set by hashing flag keys and versions
	hash := sha1.New()                                                         //nolint:gas // just used for insecure hashing
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
func bulkEventHandler(sdkKind basictypes.SDKKind, eventsKind ldevents.EventDataKind, offline bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if offline {
			w.WriteHeader(http.StatusAccepted)
			if req.Body != nil {
				_ = req.Body.Close()
			}
			return
		}

		clientCtx := middleware.GetEnvContextInfo(req.Context())
		dispatcher := clientCtx.Env.GetEventDispatcher()
		if dispatcher == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(util.ErrorJSONMsg("Event proxy is not enabled for this environment"))
			return
		}
		handler := dispatcher.GetHandler(sdkKind, eventsKind)
		if handler == nil {
			// Note, if this ever happens, it is a programming error since we are only supposed to
			// be using a fixed set of Endpoint values that the dispatcher knows about.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(util.ErrorJSONMsg("Internal error in event proxy"))
			logging.GetGlobalContextLoggers(req.Context()).Errorf("Tried to proxy %s events for %s but no handler was defined",
				eventsKind, sdkKind)
			return
		}
		handler(w, req)
	})
}

// Client-side evaluation endpoint, new schema with metadata:
// /sdk/evalx/{envId}/contexts/{context} (GET)
// /sdk/evalx/{envId}/context (REPORT)
// /sdk/evalx/{envId}/users/{context} (GET)
// /sdk/evalx/{envId}/user (REPORT)
// /sdk/evalx/users/{context} (GET - with SDK key auth; this is a Relay-only endpoint)
// /sdk/evalx/user (REPORT - with SDK key auth; this is a Relay-only endpoint)
func evaluateAllFeatureFlags(sdkKind basictypes.SDKKind) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		evaluateAllShared(w, req, false, sdkKind)
	}
}

// Client-side evaluation endpoint, old schema with only values:
// /sdk/eval/{envId}/users/{context} (GET)
// /sdk/eval/{envId}/user (REPORT)
// /sdk/eval/users/{context} (GET - with SDK key auth; this is a Relay-only endpoint)
// /sdk/eval/user (REPORT - with SDK key auth; this is a Relay-only endpoint)
func evaluateAllFeatureFlagsValueOnly(sdkKind basictypes.SDKKind) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		evaluateAllShared(w, req, true, sdkKind)
	}
}

func evaluateAllShared(w http.ResponseWriter, req *http.Request, valueOnly bool, sdkKind basictypes.SDKKind) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	client := clientCtx.Env.GetClient()
	store := clientCtx.Env.GetStore()
	loggers := clientCtx.Env.GetLoggers()

	ldContext, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, req, w)
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

	if !ldContext.Multiple() && ldContext.Key() == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg("User must have a 'key' attribute"))
		return
	}

	loggers.Debugf("Application requested client-side flags (%s) for context: %s", sdkKind, ldContext.Key())

	items, err := store.GetAll(ldstoreimpl.Features())
	if err != nil {
		loggers.Warnf("Unable to fetch flags from feature store. Returning nil map. Error: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(util.ErrorJSONMsgf("Error fetching flags from feature store: %s", err))
		return
	}

	evaluator := clientCtx.Env.GetEvaluator()

	responseWriter := jwriter.NewWriter()
	responseObj := responseWriter.Object()
	for _, item := range items {
		if flag, ok := item.Item.Item.(*ldmodel.FeatureFlag); ok {
			switch sdkKind {
			case basictypes.JSClientSDK:
				if !flag.ClientSideAvailability.UsingEnvironmentID {
					continue
				}
			case basictypes.MobileSDK:
				if !flag.ClientSideAvailability.UsingMobileKey {
					continue
				}
			}
			result := evaluator.Evaluate(flag, ldContext, nil)
			detail := result.Detail
			if valueOnly {
				detail.Value.WriteToJSONWriter(responseObj.Name(flag.Key))
			} else {
				isExperiment := result.IsExperiment
				valueObj := responseObj.Name(flag.Key).Object()
				detail.Value.WriteToJSONWriter(valueObj.Name("value"))
				detail.VariationIndex.WriteToJSONWriter(valueObj.Name("variation"))
				valueObj.Name("version").Int(flag.Version)
				valueObj.Maybe("trackEvents", flag.TrackEvents || isExperiment).Bool(true)
				valueObj.Maybe("trackReason", isExperiment).Bool(true)
				if withReasons || isExperiment {
					detail.Reason.WriteToJSONWriter(valueObj.Name("reason"))
				}
				valueObj.Maybe("debugEventsUntilDate", flag.DebugEventsUntilDate != 0).
					Float64(float64(flag.DebugEventsUntilDate))
				valueObj.End()
			}
		}
	}
	responseObj.End()
	result := responseWriter.Bytes()

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
			bytes, err := json.Marshal(item.Item)
			if err == nil {
				writeCacheableJSONResponse(w, req, clientContext, bytes, strconv.Itoa(item.Version))
			} else {
				clientContext.GetLoggers().Errorf("Error marshaling JSON: %s", err)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}
	}
}

func writeCacheableJSONResponse(w http.ResponseWriter, req *http.Request, clientContext relayenv.EnvContext,
	bytes []byte, etagValue string) {
	etag := fmt.Sprintf("relay-%s", etagValue) // just to make it extra clear that these are relay-specific etags
	if cachedEtag := req.Header.Get("If-None-Match"); cachedEtag != "" {
		if cachedEtag == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
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

func serializeFlagsAsMap(coll []ldstoretypes.KeyedItemDescriptor) []byte {
	w := jwriter.NewWriter()
	obj := w.Object()
	for _, item := range coll {
		if item.Item.Item != nil {
			ldmodel.MarshalFeatureFlagToJSONWriter(*item.Item.Item.(*ldmodel.FeatureFlag), obj.Name(item.Key))
		}
	}
	obj.End()
	return w.Bytes()
}
