package relay

import (
	"context"
	"crypto/sha1" //nolint:gosec // the hash is a cache validator, not a security control
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/launchdarkly/ld-relay/v9/internal/middleware"
	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Flight-group keys for the FDv1 poll. The Etag flight reads the store and hashes it, for the
// Etag check. The payload flight reads the store again and serializes it, for a full transfer. Its
// key is the payload key, a colon, and the Etag that the request saw.
const (
	serverSideLatestAllEtagFlightKey    = "sdk-latest-all-etag"
	serverSideLatestAllPayloadFlightKey = "sdk-latest-all"
)

// latestAllData is the live data set that the FDv1 poll serves, and the Etag that identifies it.
type latestAllData struct {
	flags    []ldstoretypes.KeyedItemDescriptor
	segments []ldstoretypes.KeyedItemDescriptor
	etag     string
}

// Server-side FDv1 polling endpoint: sdk.launchdarkly.com/sdk/latest-all
//
// The response is the full data set as {"flags": {...}, "segments": {...}}. An SDK that sends
// the Etag of its current data gets a 304 reply. That reply does not use a slot of the
// initialization-delivery budget. Only a full payload takes a slot, the same as the FDv2 poll.
func pollLatestAllHandler(w http.ResponseWriter, req *http.Request) {
	env := middleware.GetEnvContextInfo(req.Context()).Env
	tr := tracing.Tracer()

	// An FDv1 SDK takes any 200 reply as a complete data set. Before Relay has data, a 503
	// keeps the SDK retrying, so it does not start with no flags.
	if !env.GetStore().IsInitialized() {
		env.GetLogger().Debug("FDv1 poll received before the data store is initialized")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(util.ErrorJSONMsg("Service not initialized"))
		return
	}

	// Concurrent requests share one store read for the Etag check. The flight returns only
	// the Etag, so a request that gets a 304 reply keeps no data set in memory. The flight
	// does not annotate the request span, so the singleflight attributes on that span
	// describe only the payload flight.
	etag, err, _ := env.GetPollingFlightGroup().Do(serverSideLatestAllEtagFlightKey,
		func() (any, error) {
			data, err := readLatestAllData(req.Context(), tr, env)
			return data.etag, err
		})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if req.Header.Get("If-None-Match") == formatEtag(etag.(string)) {
		traceWriteResponse(tr, req, func() (int, error) {
			return writeCacheableJSONResponse(w, req, 0, nil, etag.(string))
		})
		return
	}

	// This is a full transfer. Take the slot before the serialization, so that the slot
	// accounts for the payload memory. Keep the slot until the handler returns, so that a
	// slow client cannot multiply resident payloads.
	release, ok := middleware.AcquireInitSlotFromContext(w, req)
	if !ok {
		return
	}
	defer release()

	// The payload flight reads the store again, after the wait for a slot. A request joins only
	// a flight of requests that saw the same Etag, and that flight read the store after one of
	// them saw it. So the reply has the data that the Etag check saw, or newer data. The data
	// can still change after the flight reads it.
	payloadKey := serverSideLatestAllPayloadFlightKey + ":" + etag.(string)
	result, err := runPollingFlight(req, env, payloadKey, func() (pollResult, error) {
		data, err := readLatestAllData(req.Context(), tr, env)
		if err != nil {
			return pollResult{}, err
		}
		return buildLatestAllPayload(req.Context(), tr, env, data)
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	traceWriteResponse(tr, req, func() (int, error) {
		return writeCacheableJSONResponse(w, req, 0, result.data, result.etag)
	})
}

// readLatestAllData reads the store snapshot and computes the Etag of the live data. It does
// not serialize the data. It runs inside a flight, so it logs and traces a failure one time
// for each flight. The selector cannot
// be the Etag: it is not defined while Relay serves data from a persistent store, and in
// offline mode its state does not change when the archive changes.
func readLatestAllData(ctx context.Context, tr trace.Tracer, env relayenv.EnvContext) (latestAllData, error) {
	_, storeSpan := tr.Start(ctx, tracing.SpanStoreSnapshot)
	snapshot, _, err := env.GetStore().Snapshot()
	if err != nil {
		storeSpan.RecordError(err)
		storeSpan.SetStatus(codes.Error, err.Error())
		storeSpan.End()
		env.GetLogger().Error("error reading feature store", "error", err)
		return latestAllData{}, err
	}
	data := latestAllData{
		flags:    liveItems(snapshot[ldstoreimpl.Features()]),
		segments: liveItems(snapshot[ldstoreimpl.Segments()]),
	}
	data.etag = latestAllEtag(data.flags, data.segments)
	storeSpan.End()
	return data, nil
}

// liveItems returns the items that are not deleted. A full data set has no use for the
// placeholders of deleted items, and an FDv1 SDK reads a null item as invalid data.
func liveItems(items []ldstoretypes.KeyedItemDescriptor) []ldstoretypes.KeyedItemDescriptor {
	ret := make([]ldstoretypes.KeyedItemDescriptor, 0, len(items))
	for _, item := range items {
		if item.Item.Item != nil {
			ret = append(ret, item)
		}
	}
	return ret
}

// latestAllEtag hashes the key and version of each flag and segment. The store returns items
// in no fixed order, so the hash input is sorted. A change to any item changes its version,
// and a deletion removes its key, so either one changes the Etag.
func latestAllEtag(flags, segments []ldstoretypes.KeyedItemDescriptor) string {
	lines := make([]string, 0, len(flags)+len(segments))
	for _, item := range flags {
		lines = append(lines, "flag:"+item.Key+":"+strconv.Itoa(item.Item.Version))
	}
	for _, item := range segments {
		lines = append(lines, "segment:"+item.Key+":"+strconv.Itoa(item.Item.Version))
	}
	slices.Sort(lines)
	hash := sha1.Sum([]byte(strings.Join(lines, "\n"))) //nolint:gosec // a cache validator only
	return hex.EncodeToString(hash[:])[:15]
}

// buildLatestAllPayload serializes the FDv1 poll payload. It runs inside the polling flight
// group of the environment, so it must not touch the ResponseWriter of one request. It logs
// and traces a failure one time for each flight, and every request that shares the flight
// replies with a 500.
func buildLatestAllPayload(ctx context.Context, tr trace.Tracer, env relayenv.EnvContext, data latestAllData) (pollResult, error) {
	_, span := tr.Start(ctx, tracing.SpanSerializePayload)
	defer span.End()

	fail := func(err error) (pollResult, error) {
		env.GetLogger().Error(err.Error())
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return pollResult{}, err
	}

	w := jwriter.NewWriter()
	obj := w.Object()
	flagsObj := obj.Name("flags").Object()
	for _, item := range data.flags {
		flag, ok := item.Item.Item.(*ldmodel.FeatureFlag)
		if !ok {
			return fail(errors.New("error casting keyed item to feature flag"))
		}
		ldmodel.MarshalFeatureFlagToJSONWriter(*flag, flagsObj.Name(item.Key))
	}
	flagsObj.End()
	segmentsObj := obj.Name("segments").Object()
	for _, item := range data.segments {
		segment, ok := item.Item.Item.(*ldmodel.Segment)
		if !ok {
			return fail(errors.New("error casting keyed item to feature segment"))
		}
		ldmodel.MarshalSegmentToJSONWriter(*segment, segmentsObj.Name(item.Key))
	}
	segmentsObj.End()
	obj.End()

	if err := w.Error(); err != nil {
		return fail(err)
	}
	payload := w.Bytes()
	span.SetAttributes(
		tracing.FlagCountKey.Int(len(data.flags)),
		tracing.SegmentCountKey.Int(len(data.segments)),
		tracing.PayloadSizeKey.Int(len(payload)),
	)
	return pollResult{data: payload, etag: data.etag}, nil
}
