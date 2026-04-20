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

	ldeval "github.com/launchdarkly/go-server-sdk-evaluation/v3"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/logging"
	"github.com/launchdarkly/ld-relay/v9/internal/middleware"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldreason"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

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

	if req.Method == "REPORT" || req.Method == "POST" {
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
		_, _ = w.Write(util.ErrorJSONMsg(contextDecodeErr.Error())) //nolint:gosec
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
func pingStreamHandlerV1(streamProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLogger().Debug("application requested client-side ping stream")
		clientCtx.Env.GetStreamHandlerV1(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
	})
}

// sdkKindFromCredential determines the SDK kind based on the credential type.
func sdkKindFromCredential(cred credential.SDKCredential) basictypes.SDKKind {
	switch cred.(type) {
	case config.MobileKey:
		return basictypes.MobileSDK
	default:
		return basictypes.JSClientSDK
	}
}

// This handler is used for client-side streaming endpoints that require context properties. Currently it is
// implemented the same as the ping stream once we have validated the context.
func pingStreamHandlerWithContextV1(sdkKind basictypes.SDKKind, streamProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLogger().Debug("application requested client-side ping stream")

		if _, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, req, w); ok {
			clientCtx.Env.GetStreamHandlerV1(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
		}
	})
}

// pingStreamHandlerWithContextV2 handles FDv2 client-side ping streams. It accepts two stream providers
// (mobile and JS client) and selects the appropriate one based on the credential type.
func pingStreamHandlerWithContextV2(mobileProvider, jsClientProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLogger().Debug("application requested client-side ping stream (FDv2)")

		sdkKind := sdkKindFromCredential(clientCtx.Credential)
		var streamProvider streams.StreamProvider
		if sdkKind == basictypes.MobileSDK {
			streamProvider = mobileProvider
		} else {
			streamProvider = jsClientProvider
		}

		if _, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, req, w); ok {
			clientCtx.Env.GetStreamHandlerV2(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
		}
	})
}

// Multi-purpose streaming handler; all details of the behavior of the particular type of stream are
// abstracted in StreamProvider and EnvStreams
func streamHandlerV1(streamProvider streams.StreamProvider, logMessage string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLogger().Debug(logMessage)
		clientCtx.Env.GetStreamHandlerV1(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
	})
}

// Multi-purpose streaming handler; all details of the behavior of the particular type of stream are
// abstracted in StreamProvider and EnvStreams
func streamHandlerV2(streamProvider streams.StreamProvider, logMessage string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLogger().Debug(logMessage)
		clientCtx.Env.GetStreamHandlerV2(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
	})
}

type pollingPayload struct {
	Events []payloadEvent `json:"events"`
}

type payloadEvent struct {
	Event     string `json:"event"`
	EventData any    `json:"data"`
}

// Server-side SDK polling endpoint: app.ld.com/sdk/poll/
func pollHandlerV2(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	collection, selector, err := clientCtx.Env.GetStore().Snapshot()
	if err != nil {
		clientCtx.Env.GetLogger().Error("error reading feature store", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if collection == nil || !selector.IsDefined() {
		clientCtx.Env.GetLogger().Error("snapshot selector is not defined; no data to return")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	numItems := 2
	if len(collection) > 0 {
		for _, keyedItems := range collection {
			numItems += len(keyedItems)
		}
	}

	pollingPayload := pollingPayload{
		Events: make([]payloadEvent, 0, numItems),
	}

	basis := req.URL.Query().Get("basis")
	if selector.IsDefined() && basis != "" && selector.State() == basis {
		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event: "server-intent",
			EventData: subsystems.ServerIntent{Payload: subsystems.Payload{
				ID:     selector.State(),
				Target: selector.Version(),
				Code:   subsystems.IntentNone,
				Reason: "up-to-date",
			}},
		})
	} else {
		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event: "server-intent",
			EventData: subsystems.ServerIntent{Payload: subsystems.Payload{
				ID:     selector.State(),
				Target: selector.Version(),
				Code:   subsystems.IntentTransferFull,
				Reason: "cant-catchup",
			}},
		})
		for kind, keyedItems := range collection {
			for _, keyedItem := range keyedItems {
				if keyedItem.Item.Item == nil {
					continue // this should not happen, but just in case
				}
				switch kind {
				case ldstoreimpl.Features():
					if flag, ok := keyedItem.Item.Item.(*ldmodel.FeatureFlag); ok {
						writer := jwriter.NewWriter()
						ldmodel.MarshalFeatureFlagToJSONWriter(*flag, &writer)

						pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
							Event: "put-object",
							EventData: subsystems.PutObject{
								Version: keyedItem.Item.Version,
								Kind:    subsystems.FlagKind,
								Key:     keyedItem.Key,
								Object:  writer.Bytes(),
							},
						})
					} else {
						clientCtx.Env.GetLogger().Error("error casting keyed item to feature flag")
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				case ldstoreimpl.Segments():
					if segment, ok := keyedItem.Item.Item.(*ldmodel.Segment); ok {
						writer := jwriter.NewWriter()
						ldmodel.MarshalSegmentToJSONWriter(*segment, &writer)

						pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
							Event: "put-object",
							EventData: subsystems.PutObject{
								Version: keyedItem.Item.Version,
								Kind:    subsystems.SegmentKind,
								Key:     keyedItem.Key,
								Object:  writer.Bytes(),
							},
						})
					} else {
						clientCtx.Env.GetLogger().Error("error casting keyed item to feature segment")
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				default:
					clientCtx.Env.GetLogger().Error("unexpected data kind in store snapshot", "kind", kind)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
		}
		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event:     "payload-transferred",
			EventData: selector,
		})
	}

	json, err := json.Marshal(pollingPayload)
	if err != nil {
		clientCtx.Env.GetLogger().Error("error marshaling polling response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeCacheableJSONResponse(w, req, clientCtx.Env, json, selector.State())
}

// FDv2 client-side polling endpoint that evaluates flags against a context.
func pollEvalHandlerV2(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	client := clientCtx.Env.GetClient()
	store := clientCtx.Env.GetStore()
	logger := clientCtx.Env.GetLogger()

	sdkKind := sdkKindFromCredential(clientCtx.Credential)

	ldContext, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, req, w)
	if !ok {
		return
	}

	withReasons := req.URL.Query().Get("withReasons") == "true"

	if !client.Initialized() {
		if store.IsInitialized() {
			logger.Warn("called before client initialization; using last known values from feature store")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			logger.Warn("called before client initialization, feature store not available")
			_, _ = w.Write(util.ErrorJSONMsg("Service not initialized"))
			return
		}
	}

	if !ldContext.Multiple() && ldContext.Key() == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg("User must have a 'key' attribute"))
		return
	}

	collection, selector, err := store.Snapshot()
	if err != nil {
		logger.Error("error reading feature store", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if collection == nil || !selector.IsDefined() {
		logger.Error("snapshot selector is not defined; no data to return")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	pollingPayload := pollingPayload{
		Events: make([]payloadEvent, 0),
	}

	basis := req.URL.Query().Get("basis")
	if selector.IsDefined() && basis != "" && selector.State() == basis {
		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event: "server-intent",
			EventData: subsystems.ServerIntent{Payload: subsystems.Payload{
				ID:     selector.State(),
				Target: selector.Version(),
				Code:   subsystems.IntentNone,
				Reason: "up-to-date",
			}},
		})
	} else {
		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event: "server-intent",
			EventData: subsystems.ServerIntent{Payload: subsystems.Payload{
				ID:     selector.State(),
				Target: selector.Version(),
				Code:   subsystems.IntentTransferFull,
				Reason: "cant-catchup",
			}},
		})

		evaluator := clientCtx.Env.GetEvaluator()
		flagEvalKind := subsystems.ObjectKind("flag-eval")

		var allItems []ldstoretypes.KeyedItemDescriptor
		for kind, keyedItems := range collection {
			if kind == ldstoreimpl.Features() {
				allItems = keyedItems
				break
			}
		}

		evalResults := evaluateFlags(evaluator, allItems, sdkKind, ldContext)
		for _, er := range evalResults {
			evalWriter := jwriter.NewWriter()
			evalObj := evalWriter.Object()
			er.Detail.Value.WriteToJSONWriter(evalObj.Name("value"))
			er.Detail.VariationIndex.WriteToJSONWriter(evalObj.Name("variation"))
			evalObj.Name("flagVersion").Int(er.Flag.Version)
			writePrerequisites(&evalObj, er.Prerequisites)
			evalObj.Maybe("trackEvents", er.Flag.TrackEvents || er.IsExperiment).Bool(true)
			evalObj.Maybe("trackReason", er.IsExperiment).Bool(true)
			if withReasons || er.IsExperiment {
				er.Detail.Reason.WriteToJSONWriter(evalObj.Name("reason"))
			}
			evalObj.Maybe("debugEventsUntilDate", er.Flag.DebugEventsUntilDate != 0).
				Float64(float64(er.Flag.DebugEventsUntilDate))
			if er.Flag.SamplingRatio.IsDefined() {
				evalObj.Name("samplingRatio").Int(er.Flag.SamplingRatio.IntValue())
			}
			evalObj.End()

			pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
				Event: "put-object",
				EventData: subsystems.PutObject{
					Version: er.Flag.Version,
					Kind:    flagEvalKind,
					Key:     er.Flag.Key,
					Object:  evalWriter.Bytes(),
				},
			})
		}

		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event:     "payload-transferred",
			EventData: selector,
		})
	}

	jsonData, err := json.Marshal(pollingPayload)
	if err != nil {
		logger.Error("error marshaling polling response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	writeCacheableJSONResponse(w, req, clientCtx.Env, jsonData, selector.State())
}

// PHP SDK polling endpoint for all flags: app.ld.com/sdk/flags
func pollAllFlagsHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	data, err := clientCtx.Env.GetStore().GetAll(ldstoreimpl.Features())
	if err != nil {
		clientCtx.Env.GetLogger().Error("error reading feature store", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	respData := serializeFlagsAsMap(data)
	// Compute an overall Etag for the data set by hashing flag keys and versions
	hash := sha1.New()                                                         //nolint:gosec // just used for insecure hashing
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
			logging.GetContextLogger(req.Context()).Error("tried to proxy events but no handler was defined",
				"eventsKind", eventsKind, "sdkKind", sdkKind)
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
		evaluateAllShared(w, req, sdkKind)
	}
}

// flagEvalResult holds the result of evaluating a single flag against a context.
type flagEvalResult struct {
	Flag          *ldmodel.FeatureFlag
	Detail        ldreason.EvaluationDetail
	IsExperiment  bool
	Prerequisites []string
}

// evaluateFlags evaluates all flags visible to the given SDK kind against the context.
// It filters flags by ClientSideAvailability and collects prerequisites.
func evaluateFlags(
	evaluator ldeval.Evaluator,
	items []ldstoretypes.KeyedItemDescriptor,
	sdkKind basictypes.SDKKind,
	ldContext ldcontext.Context,
) []flagEvalResult {
	var results []flagEvalResult
	for _, item := range items {
		flag, ok := item.Item.Item.(*ldmodel.FeatureFlag)
		if !ok || flag == nil {
			continue
		}

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

		var prerequisites []string
		result := evaluator.Evaluate(flag, ldContext, func(event ldeval.PrerequisiteFlagEvent) {
			if event.TargetFlagKey == flag.Key {
				prerequisites = append(prerequisites, event.PrerequisiteFlag.Key)
			}
		})

		results = append(results, flagEvalResult{
			Flag:          flag,
			Detail:        result.Detail,
			IsExperiment:  result.IsExperiment,
			Prerequisites: prerequisites,
		})
	}
	return results
}

// writePrerequisites writes a prerequisites JSON array to the given object if non-empty.
func writePrerequisites(obj *jwriter.ObjectState, prerequisites []string) {
	if len(prerequisites) > 0 {
		prereqArray := obj.Name("prerequisites").Array()
		for _, p := range prerequisites {
			prereqArray.String(p)
		}
		prereqArray.End()
	}
}

func evaluateAllShared(w http.ResponseWriter, req *http.Request, sdkKind basictypes.SDKKind) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	client := clientCtx.Env.GetClient()
	store := clientCtx.Env.GetStore()
	logger := clientCtx.Env.GetLogger()

	ldContext, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, req, w)
	if !ok {
		return
	}

	withReasons := req.URL.Query().Get("withReasons") == "true"

	w.Header().Set("Content-Type", "application/json")

	if !client.Initialized() {
		if store.IsInitialized() {
			logger.Warn("called before client initialization; using last known values from feature store")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			logger.Warn("called before client initialization, feature store not available")
			_, _ = w.Write(util.ErrorJSONMsg("Service not initialized"))
			return
		}
	}

	if !ldContext.Multiple() && ldContext.Key() == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(util.ErrorJSONMsg("User must have a 'key' attribute"))
		return
	}

	logger.Debug("application requested client-side flags", "sdkKind", sdkKind, "contextKey", ldContext.Key())

	items, err := store.GetAll(ldstoreimpl.Features())
	if err != nil {
		logger.Warn("unable to fetch flags from feature store, returning nil map", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(util.ErrorJSONMsgf("Error fetching flags from feature store: %s", err))
		return
	}

	evaluator := clientCtx.Env.GetEvaluator()
	evalResults := evaluateFlags(evaluator, items, sdkKind, ldContext)

	responseWriter := jwriter.NewWriter()
	responseObj := responseWriter.Object()
	for _, er := range evalResults {
		valueObj := responseObj.Name(er.Flag.Key).Object()
		er.Detail.Value.WriteToJSONWriter(valueObj.Name("value"))
		er.Detail.VariationIndex.WriteToJSONWriter(valueObj.Name("variation"))
		valueObj.Name("version").Int(er.Flag.Version)
		valueObj.Maybe("trackEvents", er.Flag.TrackEvents || er.IsExperiment).Bool(true)
		valueObj.Maybe("trackReason", er.IsExperiment).Bool(true)
		if withReasons || er.IsExperiment {
			er.Detail.Reason.WriteToJSONWriter(valueObj.Name("reason"))
		}
		valueObj.Maybe("debugEventsUntilDate", er.Flag.DebugEventsUntilDate != 0).
			Float64(float64(er.Flag.DebugEventsUntilDate))
		writePrerequisites(&valueObj, er.Prerequisites)
		valueObj.End()
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
			clientContext.GetLogger().Error("error reading feature store", "error", err)
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
				clientContext.GetLogger().Error("error marshaling JSON", "error", err)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}
	}
}

func writeCacheableJSONResponse(w http.ResponseWriter, req *http.Request, clientContext relayenv.EnvContext,
	bytes []byte, etagValue string,
) {
	ttl := clientContext.GetTTL()
	if ttl > 0 {
		w.Header().Set("Vary", "Authorization")
		expiresAt := time.Now().UTC().Add(ttl)
		w.Header().Set("Expires", expiresAt.Format(http.TimeFormat))
		// We're setting "Expires:" instead of "Cache-Control:max-age=" so that if someone puts an
		// HTTP cache in front of ld-relay, multiple clients hitting the cache at different times
		// will all see the same expiration time.
	}

	etag := fmt.Sprintf("W/\"%s\"", etagValue)
	if cachedEtag := req.Header.Get("If-None-Match"); cachedEtag == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Etag", etag)
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
