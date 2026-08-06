package relay

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/streams"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/go-jsonstream/v3/jwriter"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin fdv2PayloadWriter's output to the JSON that encoding/json produces for the
// same events expressed through the subsystems types, which is what the handlers marshaled
// before the single-pass encoder existed. Equality is structural: encoding/json HTML-escapes
// characters like '<' and '&' that jwriter emits raw, and both spellings decode identically.

func TestFDv2PayloadWriterFullTransferMatchesEncodingJSON(t *testing.T) {
	// The key exercises characters that the two encoders escape differently.
	flag := ldbuilders.NewFlagBuilder("flag-key-<&>").Version(3).On(true).
		SingleVariation(ldvalue.String("value")).Build()
	segment := ldbuilders.NewSegmentBuilder("segment-key").Version(5).Included("user1").Build()
	selector := subsystems.NewSelector("state-1", 42)
	intent := subsystems.Payload{
		ID:     selector.State(),
		Target: selector.Version(),
		Code:   subsystems.IntentTransferFull,
		Reason: "cant-catchup",
	}

	payloadWriter := newFDv2PayloadWriter()
	payloadWriter.writeServerIntent(intent)
	flagWriter := payloadWriter.beginPutObject(flag.Version, subsystems.FlagKind, flag.Key)
	ldmodel.MarshalFeatureFlagToJSONWriter(flag, flagWriter)
	payloadWriter.endPutObject()
	segmentWriter := payloadWriter.beginPutObject(segment.Version, subsystems.SegmentKind, segment.Key)
	ldmodel.MarshalSegmentToJSONWriter(segment, segmentWriter)
	payloadWriter.endPutObject()
	payloadWriter.writePayloadTransferred(selector)
	data, eventCount, err := payloadWriter.finish()
	require.NoError(t, err)
	assert.Equal(t, 4, eventCount)

	flagJSONWriter := jwriter.NewWriter()
	ldmodel.MarshalFeatureFlagToJSONWriter(flag, &flagJSONWriter)
	segmentJSONWriter := jwriter.NewWriter()
	ldmodel.MarshalSegmentToJSONWriter(segment, &segmentJSONWriter)
	expected, err := json.Marshal(pollingPayload{Events: []payloadEvent{
		{Event: "server-intent", EventData: subsystems.ServerIntent{Payload: intent}},
		{Event: "put-object", EventData: subsystems.PutObject{
			Version: flag.Version,
			Kind:    subsystems.FlagKind,
			Key:     flag.Key,
			Object:  flagJSONWriter.Bytes(),
		}},
		{Event: "put-object", EventData: subsystems.PutObject{
			Version: segment.Version,
			Kind:    subsystems.SegmentKind,
			Key:     segment.Key,
			Object:  segmentJSONWriter.Bytes(),
		}},
		{Event: "payload-transferred", EventData: selector},
	}})
	require.NoError(t, err)

	assert.JSONEq(t, string(expected), string(data))
}

func TestFDv2PayloadWriterUpToDateMatchesEncodingJSON(t *testing.T) {
	selector := subsystems.NewSelector("state-1", 42)
	intent := subsystems.Payload{
		ID:     selector.State(),
		Target: selector.Version(),
		Code:   subsystems.IntentNone,
		Reason: "up-to-date",
	}

	payloadWriter := newFDv2PayloadWriter()
	payloadWriter.writeServerIntent(intent)
	data, eventCount, err := payloadWriter.finish()
	require.NoError(t, err)
	assert.Equal(t, 1, eventCount)

	expected, err := json.Marshal(pollingPayload{Events: []payloadEvent{
		{Event: "server-intent", EventData: subsystems.ServerIntent{Payload: intent}},
	}})
	require.NoError(t, err)

	assert.JSONEq(t, string(expected), string(data))
}

// The polling and streaming paths encode the same protocol events with separate jwriter code:
// fdv2PayloadWriter here, and internal/streams' SSE encoders. These tests hold the two
// implementations together by running the same inputs through both and requiring identical
// event names and data JSON, so a protocol-shape change in either place fails here until
// both move. They also cover the intent payloads themselves: the streams package derives
// the up-to-date and full-transfer intents from the selector internally, so the payloads the
// polling handlers construct must match or the comparison breaks.

func TestFDv2PayloadWriterFullTransferMatchesStreamingEncoders(t *testing.T) {
	flag := ldbuilders.NewFlagBuilder("flag-key").Version(3).On(true).
		SingleVariation(ldvalue.String("value")).Build()
	segment := ldbuilders.NewSegmentBuilder("segment-key").Version(5).Included("user1").Build()
	selector := subsystems.NewSelector("state-1", 42)

	payloadWriter := newFDv2PayloadWriter()
	payloadWriter.writeServerIntent(subsystems.Payload{
		ID:     selector.State(),
		Target: selector.Version(),
		Code:   subsystems.IntentTransferFull,
		Reason: "cant-catchup",
	})
	flagWriter := payloadWriter.beginPutObject(flag.Version, subsystems.FlagKind, flag.Key)
	ldmodel.MarshalFeatureFlagToJSONWriter(flag, flagWriter)
	payloadWriter.endPutObject()
	segmentWriter := payloadWriter.beginPutObject(segment.Version, subsystems.SegmentKind, segment.Key)
	ldmodel.MarshalSegmentToJSONWriter(segment, segmentWriter)
	payloadWriter.endPutObject()
	payloadWriter.writePayloadTransferred(selector)
	doc, _, err := payloadWriter.finish()
	require.NoError(t, err)

	flagJSONWriter := jwriter.NewWriter()
	ldmodel.MarshalFeatureFlagToJSONWriter(flag, &flagJSONWriter)
	segmentJSONWriter := jwriter.NewWriter()
	ldmodel.MarshalSegmentToJSONWriter(segment, &segmentJSONWriter)
	streamEvents := streams.MakeEventsForSetBasis([]subsystems.Change{
		{
			Action:  subsystems.ChangeTypePut,
			Kind:    subsystems.FlagKind,
			Key:     flag.Key,
			Version: flag.Version,
			Object:  flagJSONWriter.Bytes(),
		},
		{
			Action:  subsystems.ChangeTypePut,
			Kind:    subsystems.SegmentKind,
			Key:     segment.Key,
			Version: segment.Version,
			Object:  segmentJSONWriter.Bytes(),
		},
	}, selector)

	assertPayloadMatchesStreamEvents(t, doc, streamEvents)
}

func TestFDv2PayloadWriterUpToDateMatchesStreamingEncoders(t *testing.T) {
	selector := subsystems.NewSelector("state-1", 42)

	payloadWriter := newFDv2PayloadWriter()
	payloadWriter.writeServerIntent(subsystems.Payload{
		ID:     selector.State(),
		Target: selector.Version(),
		Code:   subsystems.IntentNone,
		Reason: "up-to-date",
	})
	doc, _, err := payloadWriter.finish()
	require.NoError(t, err)

	assertPayloadMatchesStreamEvents(t, doc, streams.MakeEventsForUpToDate(selector))
}

func assertPayloadMatchesStreamEvents(t *testing.T, doc []byte, streamEvents []eventsource.Event) {
	t.Helper()
	var payload struct {
		Events []struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(doc, &payload))
	require.Len(t, payload.Events, len(streamEvents))
	for i, streamEvent := range streamEvents {
		assert.Equal(t, streamEvent.Event(), payload.Events[i].Event)
		assert.JSONEq(t, streamEvent.Data(), string(payload.Events[i].Data))
	}
}
