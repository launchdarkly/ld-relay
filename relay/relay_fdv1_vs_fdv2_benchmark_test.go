package relay

// Benchmarks comparing the encoding work for FDv1 initialization (one monolithic
// "put" event) against FDv2 initialization (per-item put-object events), using the
// same production-shaped dataset as the poll benchmarks. These measure the
// generation cost only, not delivery.

import (
	"testing"

	"github.com/launchdarkly/go-jsonstream/v4/jwriter"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"

	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
)

func benchSnapshotAsCollections(
	collection map[ldstoretypes.DataKind][]ldstoretypes.KeyedItemDescriptor,
) []ldstoretypes.Collection {
	return []ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: collection[ldstoreimpl.Features()]},
		{Kind: ldstoreimpl.Segments(), Items: collection[ldstoreimpl.Segments()]},
	}
}

// The FDv1 stream replay generation: one put event, all items marshaled in a single
// jwriter pass into one buffer (what getReplayEventsV1 produces via singleflight).
func BenchmarkFDv1PutEventEncode(b *testing.B) {
	collection, _ := makeBenchSnapshot()
	allData := benchSnapshotAsCollections(collection)
	size := len(streams.MakeServerSidePutEvent(allData).Data())
	b.Logf("FDv1 put event data: %d bytes, 1 SSE event", size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event := streams.MakeServerSidePutEvent(allData)
		if len(event.Data()) == 0 {
			b.Fatal("empty event")
		}
	}
}

// The FDv2 stream replay generation, mirroring getReplayEventsV2: each item is
// marshaled into its own jwriter buffer, wrapped in a Change, and then each change
// is re-encoded (with the object embedded Raw) into a put-object event string.
func BenchmarkFDv2StreamReplayEncode(b *testing.B) {
	collection, selector := makeBenchSnapshot()

	build := func() []subsystems.Change {
		changes := make([]subsystems.Change, 0, benchNumFlags+benchNumSegments)
		kinds := map[ldstoretypes.DataKind]subsystems.ObjectKind{
			ldstoreimpl.Features(): subsystems.FlagKind,
			ldstoreimpl.Segments(): subsystems.SegmentKind,
		}
		for dataKind, objectKind := range kinds {
			for _, item := range collection[dataKind] {
				if item.Item.Item == nil {
					continue
				}
				writer := jwriter.NewWriter()
				switch dataKind {
				case ldstoreimpl.Features():
					ldmodel.MarshalFeatureFlagToJSONWriter(*item.Item.Item.(*ldmodel.FeatureFlag), &writer)
				case ldstoreimpl.Segments():
					ldmodel.MarshalSegmentToJSONWriter(*item.Item.Item.(*ldmodel.Segment), &writer)
				}
				changes = append(changes, subsystems.Change{
					Action:  subsystems.ChangeTypePut,
					Kind:    objectKind,
					Key:     item.Key,
					Version: item.Item.Version,
					Object:  writer.Bytes(),
				})
			}
		}
		return changes
	}

	events := streams.MakeEventsForSetBasis(build(), selector)
	total := 0
	for _, e := range events {
		total += len(e.Data())
	}
	b.Logf("FDv2 stream replay: %d bytes across %d SSE events", total, len(events))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events := streams.MakeEventsForSetBasis(build(), selector)
		if len(events) == 0 {
			b.Fatal("no events")
		}
		// Data() is where fdv2Event encoding happened at construction; touch it to
		// keep parity with the FDv1 benchmark's Data() call.
		if len(events[1].Data()) == 0 {
			b.Fatal("empty event")
		}
	}
}

// Reports the wire sizes side by side (not a timing benchmark).
func TestFDv1VsFDv2PayloadSizes(t *testing.T) {
	collection, selector := makeBenchSnapshot()

	fdv1 := len(streams.MakeServerSidePutEvent(benchSnapshotAsCollections(collection)).Data())

	poll, err := encodeServerPollPayload(collection, selector)
	if err != nil {
		t.Fatal(err)
	}

	changes := make([]subsystems.Change, 0, benchNumFlags+benchNumSegments)
	for dataKind, objectKind := range map[ldstoretypes.DataKind]subsystems.ObjectKind{
		ldstoreimpl.Features(): subsystems.FlagKind,
		ldstoreimpl.Segments(): subsystems.SegmentKind,
	} {
		for _, item := range collection[dataKind] {
			writer := jwriter.NewWriter()
			switch dataKind {
			case ldstoreimpl.Features():
				ldmodel.MarshalFeatureFlagToJSONWriter(*item.Item.Item.(*ldmodel.FeatureFlag), &writer)
			case ldstoreimpl.Segments():
				ldmodel.MarshalSegmentToJSONWriter(*item.Item.Item.(*ldmodel.Segment), &writer)
			}
			changes = append(changes, subsystems.Change{
				Action: subsystems.ChangeTypePut, Kind: objectKind,
				Key: item.Key, Version: item.Item.Version, Object: writer.Bytes(),
			})
		}
	}
	events := streams.MakeEventsForSetBasis(changes, selector)
	fdv2Stream := 0
	for _, e := range events {
		// SSE framing per event: "event: <name>\ndata: <data>\n\n"
		fdv2Stream += len("event: ") + len(e.Event()) + 1 + len("data: ") + len(e.Data()) + 2
	}

	t.Logf("FDv1 put (1 event):        %d bytes data", fdv1)
	t.Logf("FDv2 poll body:            %d bytes", len(poll.data))
	t.Logf("FDv2 stream (%d events): %d bytes incl SSE framing", len(events), fdv2Stream)
}
