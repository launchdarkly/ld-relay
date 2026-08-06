package relay

import (
	"encoding/json"
	"testing"

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
