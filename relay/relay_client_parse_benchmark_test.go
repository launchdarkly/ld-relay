package relay

// Benchmarks approximating the SDK-side (client) parse cost of the initialization
// payloads, mirroring go-server-sdk v7:
//   - FDv1: streaming_data_source parsePutData — one jreader pass over the whole
//     "put" event, decoding each item in place.
//   - FDv2 poll: polling_http_request.Request — encoding/json unmarshal of the
//     events envelope into RawEvents, a second unmarshal per event into PutObject,
//     then a jreader decode of each Object (ChangeSet.Collections).

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly/go-jsonstream/v4/jreader"
	"github.com/launchdarkly/go-server-sdk-evaluation/v4/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/ld-relay/v9/internal/streams"
)

func BenchmarkClientParseFDv1Put(b *testing.B) {
	collection, _ := makeBenchSnapshot()
	data := []byte(streams.MakeServerSidePutEvent(benchSnapshotAsCollections(collection)).Data())
	b.Logf("FDv1 put data: %d bytes", len(data))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := jreader.NewReader(data)
		flags, segments := 0, 0
		for obj := r.Object(); obj.Next(); {
			switch string(obj.Name()) {
			case "data":
				for dataObj := r.Object(); dataObj.Next(); {
					isFlags := string(dataObj.Name()) == "flags"
					for itemsObj := r.Object(); itemsObj.Next(); {
						if isFlags {
							_ = ldmodel.UnmarshalFeatureFlagFromJSONReader(&r)
							flags++
						} else {
							_ = ldmodel.UnmarshalSegmentFromJSONReader(&r)
							segments++
						}
					}
				}
			default:
				r.SkipValue()
			}
		}
		if err := r.Error(); err != nil {
			b.Fatal(err)
		}
		if flags != benchNumFlags || segments != benchNumSegments {
			b.Fatalf("parsed %d flags %d segments", flags, segments)
		}
	}
}

func BenchmarkClientParseFDv2PollBody(b *testing.B) {
	collection, selector := makeBenchSnapshot()
	payload, err := encodeServerPollPayload(collection, selector)
	if err != nil {
		b.Fatal(err)
	}
	body := payload.data
	b.Logf("FDv2 poll body: %d bytes", len(body))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Pass 1: envelope scan into RawEvents (polling_http_request.go).
		var pp subsystems.PollingPayload
		if err := json.Unmarshal(body, &pp); err != nil {
			b.Fatal(err)
		}
		flags, segments := 0, 0
		objects := make([]json.RawMessage, 0, len(pp.Events))
		kinds := make([]subsystems.ObjectKind, 0, len(pp.Events))
		for _, ev := range pp.Events {
			if ev.Name != subsystems.EventPutObject {
				continue
			}
			// Pass 2: per-event unmarshal into PutObject.
			var put subsystems.PutObject
			if err := json.Unmarshal(ev.Data, &put); err != nil {
				b.Fatal(err)
			}
			objects = append(objects, put.Object)
			kinds = append(kinds, put.Kind)
		}
		// Pass 3: jreader decode of each object (ChangeSet.Collections / toStorableItems).
		for i, obj := range objects {
			r := jreader.NewReader(obj)
			switch kinds[i] {
			case subsystems.FlagKind:
				_ = ldmodel.UnmarshalFeatureFlagFromJSONReader(&r)
				flags++
			case subsystems.SegmentKind:
				_ = ldmodel.UnmarshalSegmentFromJSONReader(&r)
				segments++
			}
			if err := r.Error(); err != nil {
				b.Fatal(err)
			}
		}
		if flags != benchNumFlags || segments != benchNumSegments {
			b.Fatalf("parsed %d flags %d segments", flags, segments)
		}
	}
}
