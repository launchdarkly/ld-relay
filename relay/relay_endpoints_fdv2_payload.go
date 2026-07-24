package relay

import (
	"errors"
	"io"
	"sync"

	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"

	"github.com/launchdarkly/go-jsonstream/v4/jwriter"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

// This file contains the single-pass encoders for the FDv2 polling payloads and the
// per-environment cache of the full-transfer payload. The encoders produce the same
// JSON as marshaling a pollingPayload with encoding/json, but write everything in one
// jwriter pass: each store item is marshaled directly into the output buffer instead
// of into its own intermediate buffer that encoding/json would then re-scan.

const streamingEncoderBufferSize = 65536

var (
	errUnexpectedDataKind = errors.New("unexpected data kind in store snapshot")
	errItemWrongType      = errors.New("store item did not have the expected type for its kind")
)

// serializedPollPayload is a fully-encoded FDv2 polling response body for one basis.
type serializedPollPayload struct {
	state   string
	version int
	data    []byte
	events  int
}

// pollPayloadCache holds the most recently encoded full-transfer polling payload for
// one environment. For a given selector state the payload is identical for every
// client, so it only needs to be encoded once per data version. The mutex
// intentionally serializes concurrent builds: when many pollers request an uncached
// basis at once, one of them encodes while the rest block briefly and then reuse the
// cached bytes.
type pollPayloadCache struct {
	mu      sync.Mutex
	current *serializedPollPayload
}

func (c *pollPayloadCache) getOrBuild(
	selector subsystems.Selector,
	build func() (*serializedPollPayload, error),
) (payload *serializedPollPayload, cacheHit bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil && c.current.state == selector.State() && c.current.version == selector.Version() {
		return c.current, true, nil
	}
	built, err := build()
	if err != nil {
		return nil, false, err
	}
	c.current = built
	return built, false, nil
}

// Payload caches keyed by environment context. Entries for environments that are
// removed at runtime (e.g. by auto-configuration) are not evicted, so each such
// removal strands one payload; before this ships, ownership should move into the
// environment context so the cache's lifetime matches the environment's.
var pollPayloadCaches sync.Map // map[relayenv.EnvContext]*pollPayloadCache

func pollPayloadCacheForEnv(env relayenv.EnvContext) *pollPayloadCache {
	if c, ok := pollPayloadCaches.Load(env); ok {
		return c.(*pollPayloadCache)
	}
	c, _ := pollPayloadCaches.LoadOrStore(env, &pollPayloadCache{})
	return c.(*pollPayloadCache)
}

// beginPollingPayload opens the {"events":[...]} envelope and returns the array state
// that the event objects are written into.
func beginPollingPayload(w *jwriter.Writer) (jwriter.ObjectState, jwriter.ArrayState) {
	top := w.Object()
	events := top.Name("events").Array()
	return top, events
}

func endPollingPayload(top *jwriter.ObjectState, events *jwriter.ArrayState) {
	events.End()
	top.End()
}

func writeServerIntentEvent(events *jwriter.ArrayState, selector subsystems.Selector, code subsystems.IntentCode, reason string) {
	event := events.Object()
	event.Name("event").String("server-intent")
	data := event.Name("data").Object()
	payloads := data.Name("payloads").Array()
	payloadObj := payloads.Object()
	payloadObj.Name("id").String(selector.State())
	payloadObj.Name("target").Int(selector.Version())
	payloadObj.Name("intentCode").String(string(code))
	payloadObj.Name("reason").String(reason)
	payloadObj.End()
	payloads.End()
	data.End()
	event.End()
}

func writePayloadTransferredEvent(events *jwriter.ArrayState, selector subsystems.Selector) {
	event := events.Object()
	event.Name("event").String("payload-transferred")
	data := event.Name("data").Object()
	data.Name("state").String(selector.State())
	data.Name("version").Int(selector.Version())
	data.End()
	event.End()
}

// beginPutObjectEvent writes the fixed fields of a put-object event and leaves the
// data object open, positioned at the "object" property, so the caller can marshal
// the object value directly into the output. Callers must End() the returned states
// in order (data, then event).
func beginPutObjectEvent(
	events *jwriter.ArrayState, kind subsystems.ObjectKind, key string, version int,
) (jwriter.ObjectState, jwriter.ObjectState) {
	event := events.Object()
	event.Name("event").String("put-object")
	data := event.Name("data").Object()
	data.Name("version").Int(version)
	data.Name("kind").String(string(kind))
	data.Name("key").String(key)
	return event, data
}

// writeServerPollPayloadEvents encodes the events for a full transfer of the given
// snapshot: server-intent, one put-object per store item, payload-transferred.
// It returns the event count.
func writeServerPollPayloadEvents(
	w *jwriter.Writer,
	collection map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
	selector subsystems.Selector,
) (int, error) {
	top, events := beginPollingPayload(w)
	numEvents := 1
	writeServerIntentEvent(&events, selector, subsystems.IntentTransferFull, "cant-catchup")

	for kind, keyedItems := range collection {
		for _, keyedItem := range keyedItems {
			if keyedItem.Item.Item == nil {
				continue // this should not happen, but just in case
			}
			switch kind {
			case ldstoreimpl.Features():
				flag, ok := keyedItem.Item.Item.(*ldmodel.FeatureFlag)
				if !ok {
					return numEvents, errItemWrongType
				}
				event, data := beginPutObjectEvent(&events, subsystems.FlagKind, keyedItem.Key, keyedItem.Item.Version)
				ldmodel.MarshalFeatureFlagToJSONWriter(*flag, data.Name("object"))
				data.End()
				event.End()
			case ldstoreimpl.Segments():
				segment, ok := keyedItem.Item.Item.(*ldmodel.Segment)
				if !ok {
					return numEvents, errItemWrongType
				}
				event, data := beginPutObjectEvent(&events, subsystems.SegmentKind, keyedItem.Key, keyedItem.Item.Version)
				ldmodel.MarshalSegmentToJSONWriter(*segment, data.Name("object"))
				data.End()
				event.End()
			default:
				return numEvents, errUnexpectedDataKind
			}
			numEvents++
		}
	}

	writePayloadTransferredEvent(&events, selector)
	numEvents++
	endPollingPayload(&top, &events)
	return numEvents, w.Error()
}

// encodeServerPollPayload buffers a full-transfer polling payload in memory, suitable
// for caching and reuse across requests.
func encodeServerPollPayload(
	collection map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
	selector subsystems.Selector,
) (*serializedPollPayload, error) {
	w := jwriter.NewWriter()
	numEvents, err := writeServerPollPayloadEvents(&w, collection, selector)
	if err != nil {
		return nil, err
	}
	return &serializedPollPayload{
		state:   selector.State(),
		version: selector.Version(),
		data:    w.Bytes(),
		events:  numEvents,
	}, nil
}

// encodeUpToDatePollPayload encodes the intent-only payload returned when the
// client's basis already matches the current selector.
func encodeUpToDatePollPayload(selector subsystems.Selector) *serializedPollPayload {
	w := jwriter.NewWriter()
	top, events := beginPollingPayload(&w)
	writeServerIntentEvent(&events, selector, subsystems.IntentNone, "up-to-date")
	endPollingPayload(&top, &events)
	return &serializedPollPayload{
		state:   selector.State(),
		version: selector.Version(),
		data:    w.Bytes(),
		events:  1,
	}
}

// countingWriter counts bytes passed through to the underlying writer.
type countingWriter struct {
	target io.Writer
	n      int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.target.Write(p)
	cw.n += int64(n)
	return n, err
}

// streamEvalPollPayload encodes the eval polling payload directly to target through a
// fixed-size buffer, so the response body is never held in memory as a whole. Eval
// results are per-context and cannot be cached, which is why this path streams
// instead. When upToDate is true only the intent event is written and evalResults is
// ignored. It returns the number of bytes written and the event count.
func streamEvalPollPayload(
	target io.Writer,
	evalResults []flagEvalResult,
	selector subsystems.Selector,
	withReasons bool,
	upToDate bool,
) (int64, int, error) {
	cw := &countingWriter{target: target}
	w := jwriter.NewStreamingWriter(cw, streamingEncoderBufferSize)

	top, events := beginPollingPayload(&w)
	numEvents := 1
	if upToDate {
		writeServerIntentEvent(&events, selector, subsystems.IntentNone, "up-to-date")
	} else {
		writeServerIntentEvent(&events, selector, subsystems.IntentTransferFull, "cant-catchup")
		flagEvalKind := subsystems.ObjectKind("flag-eval")
		for _, er := range evalResults {
			event, data := beginPutObjectEvent(&events, flagEvalKind, er.Flag.Key, er.Flag.Version)
			evalObj := data.Name("object").Object()
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
			data.End()
			event.End()
			numEvents++
		}
		writePayloadTransferredEvent(&events, selector)
		numEvents++
	}
	endPollingPayload(&top, &events)

	if err := w.Flush(); err != nil {
		return cw.n, numEvents, err
	}
	return cw.n, numEvents, w.Error()
}
