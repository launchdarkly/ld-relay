package relay

// Benchmarks for the FDv2 server-side polling payload serialization done by
// pollHandlerV2. The dataset is sized to match an observed production-shaped
// trace: ~3408 events, ~4.1MB of JSON.

import (
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/go-jsonstream/v4/jwriter"
	"github.com/launchdarkly/go-sdk-common/v4/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

var benchmarkBytesResult []byte

const (
	benchNumFlags    = 3000
	benchNumSegments = 406
)

func makeBenchSnapshot() (map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor, subsystems.Selector) {
	collection := map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor{}

	for i := 0; i < benchNumFlags; i++ {
		fb := ldbuilders.NewFlagBuilder(fmt.Sprintf("benchmark-flag-key-%05d", i)).
			Version(i+1).
			On(true).
			Salt("abcdef0123456789abcdef0123456789").
			Variations(ldvalue.Bool(true), ldvalue.Bool(false), ldvalue.String("fallback-variation-value")).
			FallthroughVariation(0).
			OffVariation(1)
		for r := 0; r < 3; r++ {
			fb.AddRule(ldbuilders.NewRuleBuilder().
				ID(fmt.Sprintf("rule-%d-%05d", r, i)).
				Variation(r%2).
				Clauses(
					ldbuilders.Clause("email", "in",
						ldvalue.String("user1@example.com"), ldvalue.String("user2@example.com"),
						ldvalue.String("user3@example.com")),
					ldbuilders.Clause("country", "in",
						ldvalue.String("US"), ldvalue.String("CA"), ldvalue.String("GB")),
				))
		}
		fb.AddTarget(0, "target-user-key-1", "target-user-key-2", "target-user-key-3", "target-user-key-4")
		flag := fb.Build()
		collection[ldstoreimpl.Features()] = append(collection[ldstoreimpl.Features()],
			ldstoretypes.KeyedItemDescriptor{
				Key:  flag.Key,
				Item: ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag},
			})
	}

	for i := 0; i < benchNumSegments; i++ {
		sb := ldbuilders.NewSegmentBuilder(fmt.Sprintf("benchmark-segment-key-%05d", i)).
			Version(i + 1).
			Salt("abcdef0123456789abcdef0123456789")
		for u := 0; u < 25; u++ {
			sb.Included(fmt.Sprintf("included-user-key-%05d-%03d", i, u))
		}
		segment := sb.Build()
		collection[ldstoreimpl.Segments()] = append(collection[ldstoreimpl.Segments()],
			ldstoretypes.KeyedItemDescriptor{
				Key:  segment.Key,
				Item: ldstoretypes.ItemDescriptor{Version: segment.Version, Item: &segment},
			})
	}

	return collection, subsystems.NewSelector("benchmark-state-value", 1234)
}

// serializePollPayloadCurrent mirrors the serialization closure in pollHandlerV2:
// each item is marshaled into its own jwriter buffer, wrapped in a PutObject whose
// Object field is json.RawMessage, and the whole payload is then marshaled with
// encoding/json.
func serializePollPayloadCurrent(
	collection map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
	selector subsystems.Selector,
) ([]byte, error) {
	numItems := 2
	for _, keyedItems := range collection {
		numItems += len(keyedItems)
	}

	payload := pollingPayload{Events: make([]payloadEvent, 0, numItems)}
	payload.Events = append(payload.Events, payloadEvent{
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
				continue
			}
			switch kind {
			case ldstoreimpl.Features():
				flag := keyedItem.Item.Item.(*ldmodel.FeatureFlag)
				writer := jwriter.NewWriter()
				ldmodel.MarshalFeatureFlagToJSONWriter(*flag, &writer)
				payload.Events = append(payload.Events, payloadEvent{
					Event: "put-object",
					EventData: subsystems.PutObject{
						Version: keyedItem.Item.Version,
						Kind:    subsystems.FlagKind,
						Key:     keyedItem.Key,
						Object:  writer.Bytes(),
					},
				})
			case ldstoreimpl.Segments():
				segment := keyedItem.Item.Item.(*ldmodel.Segment)
				writer := jwriter.NewWriter()
				ldmodel.MarshalSegmentToJSONWriter(*segment, &writer)
				payload.Events = append(payload.Events, payloadEvent{
					Event: "put-object",
					EventData: subsystems.PutObject{
						Version: keyedItem.Item.Version,
						Kind:    subsystems.SegmentKind,
						Key:     keyedItem.Key,
						Object:  writer.Bytes(),
					},
				})
			}
		}
	}
	payload.Events = append(payload.Events, payloadEvent{
		Event:     "payload-transferred",
		EventData: selector,
	})

	return json.Marshal(payload)
}

// serializeItemsOnly performs just the per-item jwriter marshaling from the
// current implementation, without the outer encoding/json pass.
func serializeItemsOnly(
	collection map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
) []payloadEvent {
	events := make([]payloadEvent, 0, 3500)
	for kind, keyedItems := range collection {
		for _, keyedItem := range keyedItems {
			if keyedItem.Item.Item == nil {
				continue
			}
			writer := jwriter.NewWriter()
			var objKind subsystems.ObjectKind
			switch kind {
			case ldstoreimpl.Features():
				ldmodel.MarshalFeatureFlagToJSONWriter(*keyedItem.Item.Item.(*ldmodel.FeatureFlag), &writer)
				objKind = subsystems.FlagKind
			case ldstoreimpl.Segments():
				ldmodel.MarshalSegmentToJSONWriter(*keyedItem.Item.Item.(*ldmodel.Segment), &writer)
				objKind = subsystems.SegmentKind
			}
			events = append(events, payloadEvent{
				Event: "put-object",
				EventData: subsystems.PutObject{
					Version: keyedItem.Item.Version,
					Kind:    objKind,
					Key:     keyedItem.Key,
					Object:  writer.Bytes(),
				},
			})
		}
	}
	return events
}

func TestPollPayloadImplementationsAgree(t *testing.T) {
	collection, selector := makeBenchSnapshot()
	current, err := serializePollPayloadCurrent(collection, selector)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := encodeServerPollPayload(collection, selector)
	if err != nil {
		t.Fatal(err)
	}
	singlePass := payload.data
	t.Logf("payload size: current=%d bytes, singlePass=%d bytes", len(current), len(singlePass))

	var a, b any
	if err := json.Unmarshal(current, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(singlePass, &b); err != nil {
		t.Fatal(err)
	}
	// Event order differs because map iteration order differs between runs; compare
	// as sets keyed by the serialized event.
	normalize := func(v any) map[string]int {
		events := v.(map[string]any)["events"].([]any)
		set := make(map[string]int, len(events))
		for _, e := range events {
			s, _ := json.Marshal(e)
			set[string(s)]++
		}
		return set
	}
	na, nb := normalize(a), normalize(b)
	if len(na) != len(nb) {
		t.Fatalf("event count mismatch: %d vs %d", len(na), len(nb))
	}
	for k, count := range na {
		if nb[k] != count {
			t.Fatalf("event mismatch for %.120s...", k)
		}
	}
}

func BenchmarkPollPayloadCurrent(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	data, err := serializePollPayloadCurrent(collection, selector)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("payload: %d bytes, %d events", len(data), 2+benchNumFlags+benchNumSegments)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkBytesResult, _ = serializePollPayloadCurrent(collection, selector)
	}
}

func BenchmarkPollPayloadSinglePass(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload, err := encodeServerPollPayload(collection, selector)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkBytesResult = payload.data
	}
}

// A poll served from the payload cache: the cost every request after the first pays
// for a given basis.
func BenchmarkPollPayloadCacheHit(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	cache := &pollPayloadCache{}
	build := func() (*serializedPollPayload, error) {
		return encodeServerPollPayload(collection, selector)
	}
	if _, _, err := cache.getOrBuild(selector, build); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload, hit, err := cache.getOrBuild(selector, build)
		if err != nil || !hit {
			b.Fatalf("expected cache hit, err=%v", err)
		}
		benchmarkBytesResult = payload.data
	}
}

// Measures per-request wall-clock latency (not throughput) when more concurrent
// polls than CPUs are serializing at once. Run with -cpu 2 to approximate an
// m5a.large. The custom latency-ms/op metric is the average time one request
// spends inside the serialize span under contention.
func BenchmarkPollPayloadCurrentUnderContention(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	var totalNs, ops atomic.Int64
	b.SetParallelism(4)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			start := time.Now()
			benchmarkBytesResult, _ = serializePollPayloadCurrent(collection, selector)
			totalNs.Add(time.Since(start).Nanoseconds())
			ops.Add(1)
		}
	})
	b.ReportMetric(float64(totalNs.Load())/float64(ops.Load())/1e6, "latency-ms/op")
}

// The single-pass encoding written through a fixed-size buffer to an io.Writer, as
// the streamed eval endpoint does; the full payload is never held in memory.
func BenchmarkPollPayloadStreaming(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := jwriter.NewStreamingWriter(io.Discard, streamingEncoderBufferSize)
		if _, err := writeServerPollPayloadEvents(&w, collection, selector); err != nil {
			b.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}

// The per-item jwriter stage of the current implementation, in isolation.
func BenchmarkPollPayloadStageItemsOnly(b *testing.B) {
	collection, _ := makeBenchSnapshot()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events := serializeItemsOnly(collection)
		if len(events) == 0 {
			b.Fatal("no events")
		}
	}
}

// The outer encoding/json stage of the current implementation, in isolation.
func BenchmarkPollPayloadStageOuterMarshalOnly(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	payload := pollingPayload{Events: serializeItemsOnly(collection)}
	payload.Events = append(payload.Events, payloadEvent{
		Event:     "payload-transferred",
		EventData: selector,
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkBytesResult, _ = json.Marshal(payload)
	}
}
