package relay

import (
	"context"
	"crypto/sha1" //nolint:gosec // we're not using SHA1 for encryption, just for generating an insecure hash
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldreason"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

func getClientSideContextProperties(
	clientCtx relayenv.EnvContext,
	sdkKind basictypes.SDKKind,
	maxBodySize ct.OptBase2Bytes,
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
		bodyReader := req.Body
		if maxBodySize.IsDefined() {
			bodyReader = http.MaxBytesReader(w, req.Body, int64(maxBodySize.GetOrElse(0)))
		}
		body, readErr := io.ReadAll(bodyReader)
		if readErr != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(readErr, &maxBytesErr) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_, _ = w.Write(util.ErrorJSONMsg("Request body exceeds maximum allowed size."))
				return ldContext, false
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(util.ErrorJSONMsg(readErr.Error()))
			return ldContext, false
		}
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
func pingStreamHandlerWithContextV1(sdkKind basictypes.SDKKind, maxBodySize ct.OptBase2Bytes, streamProvider streams.StreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientCtx := middleware.GetEnvContextInfo(req.Context())
		clientCtx.Env.GetLogger().Debug("application requested client-side ping stream")

		if _, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, maxBodySize, req, w); ok {
			clientCtx.Env.GetStreamHandlerV1(streamProvider, clientCtx.Credential).ServeHTTP(w, req)
		}
	})
}

// pingStreamHandlerWithContextV2 handles FDv2 client-side ping streams. It accepts two stream providers
// (mobile and JS client) and selects the appropriate one based on the credential type.
func pingStreamHandlerWithContextV2(mobileProvider, jsClientProvider streams.StreamProvider, maxBodySize ct.OptBase2Bytes) http.Handler {
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

		if _, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, maxBodySize, req, w); ok {
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

// pollResult is what one execution of a polling flight-group closure produces: the exact response
// body and the value the Etag header is derived from. Concurrent requests that share a flight all
// receive the same pollResult; nothing in it is specific to any one request.
type pollResult struct {
	data []byte
	etag string
}

// Flight-group keys for the polling endpoints. The flight group is scoped to one environment, so
// these only need to distinguish the endpoints from each other, plus any request parameter that
// changes the payload (which the handlers append).
const (
	serverSidePollFlightKey     = "sdk-poll"
	serverSideAllFlagsFlightKey = "sdk-flags"
)

// runPollingFlight resolves one polling request through the environment's flight group,
// annotating the request's span with how the flight resolved (refer to tracing.SingleflightDo).
func runPollingFlight(
	req *http.Request,
	env relayenv.EnvContext,
	key string,
	build func() (pollResult, error),
) (pollResult, error) {
	data, err := tracing.SingleflightDo(req.Context(), env.GetPollingFlightGroup(), key, func() (any, error) {
		result, err := build()
		if err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		return pollResult{}, err
	}

	// panic if it's not a pollResult - as this should be impossible
	return data.(pollResult), nil
}

// Server-side SDK polling endpoint: app.ld.com/sdk/poll/
func pollHandlerV2(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	tr := tracing.Tracer()

	basis := req.URL.Query().Get("basis")

	// Concurrent requests would each take a store snapshot and serialize an identical payload;
	// the flight group runs that work once and hands every waiting request the same result.
	// The payload depends on the caller's basis -- a basis matching the current selector state
	// gets an "up-to-date" response, anything else gets a full transfer -- so only requests
	// with the same basis may share a result, and the basis is part of the key.
	//
	// The snapshot and serialize spans belong to the one request that executes the closure;
	// every request records on its own request span whether the payload build was shared, and
	// how long it waited if another request built it.
	result, err := runPollingFlight(req, clientCtx.Env, serverSidePollFlightKey+":"+basis, func() (pollResult, error) {
		return buildServerSidePollPayload(req.Context(), tr, clientCtx.Env, basis)
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	traceWriteResponse(tr, req, func() (int, error) {
		return writeCacheableJSONResponse(w, req, clientCtx.Env, result.data, result.etag)
	})
}

// buildServerSidePollPayload takes the store snapshot and serializes the FDv2 polling payload for
// pollHandlerV2. It runs inside the environment's polling flight group, so it must not touch any
// one request's ResponseWriter; failures are logged and traced here (once per flight, not once
// per waiting request) and returned as a plain error that every sharing request maps to a 500.
func buildServerSidePollPayload(
	ctx context.Context,
	tr trace.Tracer,
	env relayenv.EnvContext,
	basis string,
) (pollResult, error) {
	_, storeSpan := tr.Start(ctx, tracing.SpanStoreSnapshot)
	collection, selector, err := env.GetStore().Snapshot()
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.SetStatus(codes.Error, err.Error())
	}
	storeSpan.End()

	if err != nil {
		env.GetLogger().Error("error reading feature store", "error", err)
		return pollResult{}, err
	} else if collection == nil || !selector.IsDefined() {
		err := errors.New("snapshot selector is not defined; no data to return")
		env.GetLogger().Error(err.Error())
		return pollResult{}, err
	}

	_, span := tr.Start(ctx, tracing.SpanSerializePayload)
	defer span.End()

	numItems := 2
	if len(collection) > 0 {
		for _, keyedItems := range collection {
			numItems += len(keyedItems)
		}
	}

	pollingPayload := pollingPayload{
		Events: make([]payloadEvent, 0, numItems),
	}

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
						err := errors.New("error casting keyed item to feature flag")
						env.GetLogger().Error(err.Error())
						span.RecordError(err)
						span.SetStatus(codes.Error, err.Error())
						return pollResult{}, err
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
						err := errors.New("error casting keyed item to feature segment")
						env.GetLogger().Error(err.Error())
						span.RecordError(err)
						span.SetStatus(codes.Error, err.Error())
						return pollResult{}, err
					}
				default:
					err := errors.New("unexpected data kind in store snapshot")
					env.GetLogger().Error(err.Error(), "kind", kind)
					span.RecordError(err)
					span.SetStatus(codes.Error, err.Error())
					return pollResult{}, err
				}
			}
		}
		pollingPayload.Events = append(pollingPayload.Events, payloadEvent{
			Event:     "payload-transferred",
			EventData: selector,
		})
	}

	data, err := json.Marshal(pollingPayload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		env.GetLogger().Error("error marshaling polling response", "error", err)
		return pollResult{}, err
	}
	span.SetAttributes(
		tracing.PayloadEventsKey.Int(len(pollingPayload.Events)),
		tracing.PayloadBytesKey.Int(len(data)),
	)
	return pollResult{data: data, etag: selector.State()}, nil
}

// FDv2 client-side polling endpoint that evaluates flags against a context.
func pollEvalHandlerV2(maxBodySize ct.OptBase2Bytes) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		pollEvalHandlerV2Shared(w, req, maxBodySize)
	}
}

func pollEvalHandlerV2Shared(w http.ResponseWriter, req *http.Request, maxBodySize ct.OptBase2Bytes) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	tr := tracing.Tracer()
	client := clientCtx.Env.GetClient()
	store := clientCtx.Env.GetStore()
	logger := clientCtx.Env.GetLogger()

	sdkKind := sdkKindFromCredential(clientCtx.Credential)

	ldContext, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, maxBodySize, req, w)
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

	_, storeSpan := tr.Start(req.Context(), tracing.SpanStoreSnapshot)
	collection, selector, err := store.Snapshot()
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.SetStatus(codes.Error, err.Error())
	}
	storeSpan.End()

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
	flagEvalKind := subsystems.ObjectKind("flag-eval")

	basis := req.URL.Query().Get("basis")
	upToDate := selector.IsDefined() && basis != "" && selector.State() == basis

	var evalResults []flagEvalResult
	if upToDate {
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

		var allItems []ldstoretypes.KeyedItemDescriptor
		for kind, keyedItems := range collection {
			if kind == ldstoreimpl.Features() {
				allItems = keyedItems
				break
			}
		}

		_, evalSpan := tr.Start(req.Context(), tracing.SpanEvaluateFlags)
		evalResults = evaluateFlags(evaluator, allItems, sdkKind, ldContext)
		evalSpan.SetAttributes(tracing.FlagCountKey.Int(len(evalResults)))
		evalSpan.End()
	}

	jsonData, ok := func() ([]byte, bool) {
		_, span := tr.Start(req.Context(), tracing.SpanSerializePayload)
		defer span.End()

		if !upToDate {
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

		data, err := json.Marshal(pollingPayload)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			logger.Error("error marshaling polling response", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return nil, false
		}
		span.SetAttributes(
			tracing.PayloadEventsKey.Int(len(pollingPayload.Events)),
			tracing.PayloadBytesKey.Int(len(data)),
		)
		return data, true
	}()
	if !ok {
		return
	}

	traceWriteResponse(tr, req, func() (int, error) {
		return writeCacheableJSONResponse(w, req, clientCtx.Env, jsonData, selector.State())
	})
}

// PHP SDK polling endpoint for all flags: app.ld.com/sdk/flags
func pollAllFlagsHandler(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	tr := tracing.Tracer()

	// Concurrent requests would each read every flag and serialize an identical map; the
	// flight group runs that work once and hands every waiting request the same result. The
	// payload and Etag depend only on the store contents, so a single key covers all callers.
	// Span placement follows pollHandlerV2: the store and serialize spans belong to the one
	// request that executes the closure.
	result, err := runPollingFlight(req, clientCtx.Env, serverSideAllFlagsFlightKey, func() (pollResult, error) {
		return buildAllFlagsPayload(req.Context(), tr, clientCtx.Env)
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	traceWriteResponse(tr, req, func() (int, error) {
		return writeCacheableJSONResponse(w, req, clientCtx.Env, result.data, result.etag)
	})
}

// buildAllFlagsPayload reads every flag and serializes the all-flags map for pollAllFlagsHandler.
// It runs inside the environment's polling flight group, so it must not touch any one request's
// ResponseWriter; failures are logged and traced here (once per flight, not once per waiting
// request) and returned as a plain error that every sharing request maps to a 500.
func buildAllFlagsPayload(ctx context.Context, tr trace.Tracer, env relayenv.EnvContext) (pollResult, error) {
	_, storeSpan := tr.Start(ctx, tracing.SpanStoreGetAll)
	data, err := env.GetStore().GetAll(ldstoreimpl.Features())
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.SetStatus(codes.Error, err.Error())
	}
	storeSpan.End()

	if err != nil {
		env.GetLogger().Error("error reading feature store", "error", err)
		return pollResult{}, err
	}

	_, span := tr.Start(ctx, tracing.SpanSerializePayload)
	defer span.End()

	respData, flagCount := serializeFlagsAsMap(data)
	// Compute an overall Etag for the data set by hashing flag keys and versions
	hash := sha1.New()                                                         //nolint:gosec // just used for insecure hashing
	sort.Slice(data, func(i, j int) bool { return data[i].Key < data[j].Key }) // makes the hash deterministic
	for _, item := range data {
		_, _ = io.WriteString(hash, fmt.Sprintf("%s:%d", item.Key, item.Item.Version))
	}
	etag := hex.EncodeToString(hash.Sum(nil))[:15]
	span.SetAttributes(
		tracing.FlagCountKey.Int(flagCount),
		tracing.PayloadBytesKey.Int(len(respData)),
	)
	return pollResult{data: respData, etag: etag}, nil
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

		ctx, eventSpan := tracing.Tracer().Start(req.Context(), tracing.SpanEventsDispatch)
		defer eventSpan.End()
		eventSpan.SetAttributes(tracing.EventsKindKey.String(string(eventsKind)))
		handler(w, req.WithContext(ctx))
	})
}

// Client-side evaluation endpoint, new schema with metadata:
// /sdk/evalx/{envId}/contexts/{context} (GET)
// /sdk/evalx/{envId}/context (REPORT)
// /sdk/evalx/{envId}/users/{context} (GET)
// /sdk/evalx/{envId}/user (REPORT)
// /sdk/evalx/users/{context} (GET - with SDK key auth; this is a Relay-only endpoint)
// /sdk/evalx/user (REPORT - with SDK key auth; this is a Relay-only endpoint)
func evaluateAllFeatureFlags(sdkKind basictypes.SDKKind, maxBodySize ct.OptBase2Bytes) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		evaluateAllShared(w, req, sdkKind, maxBodySize)
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

func evaluateAllShared(w http.ResponseWriter, req *http.Request, sdkKind basictypes.SDKKind, maxBodySize ct.OptBase2Bytes) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	tr := tracing.Tracer()
	client := clientCtx.Env.GetClient()
	store := clientCtx.Env.GetStore()
	logger := clientCtx.Env.GetLogger()

	ldContext, ok := getClientSideContextProperties(clientCtx.Env, sdkKind, maxBodySize, req, w)
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

	_, storeSpan := tr.Start(req.Context(), tracing.SpanStoreGetAll)
	items, err := store.GetAll(ldstoreimpl.Features())
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.SetStatus(codes.Error, err.Error())
	}
	storeSpan.End()

	if err != nil {
		logger.Warn("unable to fetch flags from feature store, returning nil map", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(util.ErrorJSONMsgf("Error fetching flags from feature store: %s", err))
		return
	}

	evaluator := clientCtx.Env.GetEvaluator()

	_, evalSpan := tr.Start(req.Context(), tracing.SpanEvaluateFlags)
	evalResults := evaluateFlags(evaluator, items, sdkKind, ldContext)
	evalSpan.SetAttributes(tracing.FlagCountKey.Int(len(evalResults)))
	evalSpan.End()

	result := func() []byte {
		_, span := tr.Start(req.Context(), tracing.SpanSerializePayload)
		defer span.End()

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
		data := responseWriter.Bytes()
		span.SetAttributes(
			tracing.FlagCountKey.Int(len(evalResults)),
			tracing.PayloadBytesKey.Int(len(data)),
		)
		return data
	}()

	traceWriteResponse(tr, req, func() (int, error) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(result)
		return http.StatusOK, err
	})
}

func pollFlagOrSegment(clientContext relayenv.EnvContext, kind ldstoretypes.DataKind) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		key := mux.Vars(req)["key"]
		tr := tracing.Tracer()

		_, storeSpan := tr.Start(req.Context(), tracing.SpanStoreGet)
		storeSpan.SetAttributes(tracing.StoreKeyKey.String(key))
		item, err := clientContext.GetStore().Get(kind, key)
		if err != nil {
			storeSpan.RecordError(err)
			storeSpan.SetStatus(codes.Error, err.Error())
		}
		storeSpan.End()

		if err != nil {
			clientContext.GetLogger().Error("error reading feature store", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if item.Item == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		bytes, ok := func() ([]byte, bool) {
			_, span := tr.Start(req.Context(), tracing.SpanSerializePayload)
			defer span.End()
			data, err := json.Marshal(item.Item)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				clientContext.GetLogger().Error("error marshaling JSON", "error", err)
				w.WriteHeader(http.StatusInternalServerError)
				return nil, false
			}
			span.SetAttributes(tracing.PayloadBytesKey.Int(len(data)))
			return data, true
		}()
		if !ok {
			return
		}

		traceWriteResponse(tr, req, func() (int, error) {
			return writeCacheableJSONResponse(w, req, clientContext, bytes, strconv.Itoa(item.Version))
		})
	}
}

// traceWriteResponse runs a response write inside a relay.response.write span, recording the
// status code that was sent and whatever the write itself reported.
//
// Recording the error matters: a write that fails once the header is out -- a client that
// disconnected, a body that was truncated -- cannot change the status code the request span
// reports, so this span is the only place such a failure surfaces.
//
// The span deliberately carries no byte count. relay.payload.bytes on the serialize span
// reports what was built, and http.response.body.size on the request span reports what
// actually went out; the latter is counted outside the compression middleware, which is the
// only place the wire size is observable. A count taken here could only ever repeat the
// payload size.
//
// Note also that the span measures the time to hand the payload to the ResponseWriter, not
// time on the socket: a response small enough to fit net/http's output buffer is flushed after
// the handler returns.
func traceWriteResponse(tr trace.Tracer, req *http.Request, write func() (int, error)) {
	_, span := tr.Start(req.Context(), tracing.SpanWriteResponse)
	defer span.End()

	status, err := write()
	span.SetAttributes(semconv.HTTPResponseStatusCode(status))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// writeCacheableJSONResponse sends bytes as a JSON response with this environment's cache
// headers, or an empty 304 when the caller's If-None-Match matches the current Etag. It
// returns the status code that was sent and the error from writing the body, if any.
func writeCacheableJSONResponse(w http.ResponseWriter, req *http.Request, clientContext relayenv.EnvContext,
	bytes []byte, etagValue string,
) (int, error) {
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
		return http.StatusNotModified, nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Etag", etag)
	w.WriteHeader(http.StatusOK)

	// Not XSS: every caller passes the output of a JSON encoder, and the response is served as
	// application/json. The taint analysis only sees that a request parameter (the polling basis)
	// influenced which payload was built.
	_, err := w.Write(bytes) //nolint:gosec
	return http.StatusOK, err
}

// serializeFlagsAsMap writes the flags in coll as a JSON object keyed by flag key, and reports
// how many it wrote. GetAll is contractually required to include placeholders for deleted items,
// which have no flag to serialize, so the count is not len(coll).
func serializeFlagsAsMap(coll []ldstoretypes.KeyedItemDescriptor) ([]byte, int) {
	w := jwriter.NewWriter()
	obj := w.Object()
	count := 0
	for _, item := range coll {
		if item.Item.Item != nil {
			ldmodel.MarshalFeatureFlagToJSONWriter(*item.Item.Item.(*ldmodel.FeatureFlag), obj.Name(item.Key))
			count++
		}
	}
	obj.End()
	return w.Bytes(), count
}
