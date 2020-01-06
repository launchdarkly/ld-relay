package events

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
)

func TestTranslateFeatureEventWithSchemaVersion1AndExistingFlag(t *testing.T) {
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}
	date := uint64(9000)
	flag := &ld.FeatureFlag{
		Key:                  "flagkey",
		Version:              22, // deliberately different version from event - we should use the version from the event
		Variations:           []interface{}{"a", "b"},
		TrackEvents:          true,
		DebugEventsUntilDate: &date,
	}
	_ = store.Upsert(ld.Features, flag)

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"version": 11,
		"value": "b",
		"default": "c"
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 1)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, *fe.Version)
	assert.Equal(t, "b", fe.Value)
	assert.Equal(t, 1, *fe.Variation) // set by translateEvent based on flag.Variations
	assert.Equal(t, "c", fe.Default)
	assert.True(t, fe.TrackEvents) // set by translateEvent from flag.TrackEvents
	assert.Equal(t, flag.DebugEventsUntilDate, fe.DebugEventsUntilDate)
}

func TestTranslateFeatureEventWithSchemaVersion1AndUnknownFlag(t *testing.T) {
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"value": "c",
		"default": "c"
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 1)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, "flagkey", fe.Key)
	assert.Nil(t, fe.Version)
	assert.Equal(t, "c", fe.Value)
	assert.Nil(t, fe.Variation)
	assert.Equal(t, "c", fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Nil(t, fe.DebugEventsUntilDate)
}

func TestTranslateFeatureEventWithSchemaVersion1AndUnexpectedlyUnknownFlag(t *testing.T) {
	// The only difference here from the previous test is that "version" has a value, so we will try to
	// look up the flag, but the lookup will fail.
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"version": 11,
		"value": "c",
		"default": "c"
	}`
	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 1)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, "flagkey", fe.Key)
	assert.Equal(t, 11, *fe.Version)
	assert.Equal(t, "c", fe.Value)
	assert.Nil(t, fe.Variation)
	assert.Equal(t, "c", fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Nil(t, fe.DebugEventsUntilDate)
}

func TestTranslateFeatureEventWithSchemaVersion2AndExistingFlagWithoutTrackEventsInEvent(t *testing.T) {
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}
	date := uint64(9000)
	flag := &ld.FeatureFlag{
		Key:                  "flagkey",
		Version:              22,                      // deliberately different version from event - we should use the version from the event
		Variations:           []interface{}{"a", "x"}, // deliberately doesn't include "b" - we want to see that it uses the variation index from the event
		TrackEvents:          true,
		DebugEventsUntilDate: &date,
	}
	_ = store.Upsert(ld.Features, flag)

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"version": 11,
		"value": "b",
		"variation": 1,
		"default": "c",
		"reason": { "kind": "FALLTHROUGH" }
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 2)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, *fe.Version)
	assert.Equal(t, "b", fe.Value)
	assert.Equal(t, 1, *fe.Variation)
	assert.Equal(t, "c", fe.Default)
	assert.True(t, fe.TrackEvents) // set by translateEvent from flag.TrackEvents
	assert.Equal(t, flag.DebugEventsUntilDate, fe.DebugEventsUntilDate)
	assert.Equal(t, ld.EvalReasonFallthrough, fe.Reason.Reason.GetKind())
}

func TestTranslateFeatureEventWithSchemaVersion2AndExistingFlagWithTrackEventsInEvent(t *testing.T) {
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}
	flag := &ld.FeatureFlag{
		Key:        "flagkey",
		Version:    22,                      // deliberately different version from event - we should use the version from the event
		Variations: []interface{}{"a", "x"}, // deliberately doesn't include "b" - we want to see that it uses the variation index from the event
	}
	_ = store.Upsert(ld.Features, flag)

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"version": 11,
		"value": "b",
		"variation": 1,
		"default": "c",
		"reason": { "kind": "FALLTHROUGH" },
		"trackEvents": true
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 2)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, *fe.Version)
	assert.Equal(t, "b", fe.Value)
	assert.Equal(t, 1, *fe.Variation)
	assert.Equal(t, "c", fe.Default)
	assert.True(t, fe.TrackEvents) // comes from the event, not the flag
	assert.Nil(t, fe.DebugEventsUntilDate)
	assert.Equal(t, ld.EvalReasonFallthrough, fe.Reason.Reason.GetKind())
}

func TestTranslateFeatureEventWithSchemaVersion2AndExistingFlagWithDebugEventsUntilDateInEvent(t *testing.T) {
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}
	flag := &ld.FeatureFlag{
		Key:        "flagkey",
		Version:    22,                      // deliberately different version from event - we should use the version from the event
		Variations: []interface{}{"a", "x"}, // deliberately doesn't include "b" - we want to see that it uses the variation index from the event
	}
	_ = store.Upsert(ld.Features, flag)

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"version": 11,
		"value": "b",
		"variation": 1,
		"default": "c",
		"reason": { "kind": "FALLTHROUGH" },
		"debugEventsUntilDate": 9000
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 2)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, *fe.Version)
	assert.Equal(t, "b", fe.Value)
	assert.Equal(t, 1, *fe.Variation)
	assert.Equal(t, "c", fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Equal(t, uint64(9000), *fe.DebugEventsUntilDate)
	assert.Equal(t, ld.EvalReasonFallthrough, fe.Reason.Reason.GetKind())
}

func TestTranslateFeatureEventWithSchemaVersion2AndUnknownFlag(t *testing.T) {
	store, _ := ld.NewInMemoryFeatureStoreFactory()(ld.Config{})
	er := &eventSummarizingRelay{
		featureStore: store,
	}

	eventIn := `{
		"kind": "feature",
		"creationDate": 1000,
		"key": "flagkey",
		"value": "c",
		"default": "c",
		"reason": { "kind": "ERROR", "errorKind": "FLAG_NOT_FOUND" }
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 2)
	require.NoError(t, err)
	fe := eventOut.(ld.FeatureRequestEvent)
	assert.Equal(t, uint64(1000), fe.CreationDate)
	assert.Equal(t, "flagkey", fe.Key)
	assert.Nil(t, fe.Version)
	assert.Equal(t, "c", fe.Value)
	assert.Nil(t, fe.Variation)
	assert.Equal(t, "c", fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Nil(t, fe.DebugEventsUntilDate)
	assert.Equal(t, ld.EvalReasonError, fe.Reason.Reason.GetKind())
}

func TestTranslateIdentifyEventReturnsEventUnchanged(t *testing.T) {
	er := &eventSummarizingRelay{}

	eventIn := `{
		"kind": "identify",
		"creationDate": 1000,
		"key": "userkey",
		"user": {
			"key": "userkey",
			"name": "Mina"
		}
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 2)
	require.NoError(t, err)
	ie := eventOut.(ld.IdentifyEvent)
	assert.Equal(t, uint64(1000), ie.CreationDate)
	assert.Equal(t, "userkey", *ie.User.Key)
	assert.Equal(t, "Mina", *ie.User.Name)
}

func TestTranslateCustomEventReturnsEventUnchanged(t *testing.T) {
	er := &eventSummarizingRelay{}

	eventIn := `{
		"kind": "custom",
		"creationDate": 1000,
		"key": "customkey",
		"user": {
			"key": "userkey",
			"name": "Lucy"
		},
		"data": "x",
		"metricValue": 1.5
	}`

	eventOut, err := er.translateEvent(json.RawMessage(eventIn), 2)
	require.NoError(t, err)
	ce := eventOut.(ld.CustomEvent)
	assert.Equal(t, uint64(1000), ce.CreationDate)
	assert.Equal(t, "customkey", ce.Key)
	assert.Equal(t, "userkey", *ce.User.Key)
	assert.Equal(t, "Lucy", *ce.User.Name)
	assert.Equal(t, "x", ce.Data)
	assert.Equal(t, 1.5, *ce.MetricValue)
}
