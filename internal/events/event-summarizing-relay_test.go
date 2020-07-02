package events

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/sharedtest"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldreason"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	relaystore "github.com/launchdarkly/ld-relay/v6/internal/store"
)

func TestTranslateFeatureEventWithSchemaVersion1AndExistingFlag(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
	}
	flag := ldbuilders.NewFlagBuilder("flagkey").
		Version(22). // deliberately different version from event - we should use the version from the event
		Variations(ldvalue.String("a"), ldvalue.String("b")).
		TrackEvents(true).
		DebugEventsUntilDate(ldtime.UnixMillisecondTime(9000)).
		Build()
	_, _ = sharedtest.UpsertFlag(store, flag)

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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, fe.Version)
	assert.Equal(t, ldvalue.String("b"), fe.Value)
	assert.Equal(t, 1, fe.Variation) // set by translateEvent based on flag.Variations
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.True(t, fe.TrackEvents) // set by translateEvent from flag.TrackEvents
	assert.Equal(t, flag.DebugEventsUntilDate, fe.DebugEventsUntilDate)
}

func TestTranslateFeatureEventWithSchemaVersion1AndUnknownFlag(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, "flagkey", fe.Key)
	assert.Equal(t, ldevents.NoVersion, fe.Version)
	assert.Equal(t, ldvalue.String("c"), fe.Value)
	assert.Equal(t, ldevents.NoVariation, fe.Variation)
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Equal(t, ldtime.UnixMillisecondTime(0), fe.DebugEventsUntilDate)
}

func TestTranslateFeatureEventWithSchemaVersion1AndUnexpectedlyUnknownFlag(t *testing.T) {
	// The only difference here from the previous test is that "version" has a value, so we will try to
	// look up the flag, but the lookup will fail.
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, "flagkey", fe.Key)
	assert.Equal(t, 11, fe.Version)
	assert.Equal(t, ldvalue.String("c"), fe.Value)
	assert.Equal(t, ldevents.NoVariation, fe.Variation)
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Equal(t, ldtime.UnixMillisecondTime(0), fe.DebugEventsUntilDate)
}

func TestTranslateFeatureEventWithSchemaVersion2AndExistingFlagWithoutTrackEventsInEvent(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
	}
	flag := ldbuilders.NewFlagBuilder("flagkey").
		Version(22).                                          // deliberately different version from event - we should use the version from the event
		Variations(ldvalue.String("a"), ldvalue.String("x")). // deliberately doesn't include "b" - we want to see that it uses the variation index from the event
		TrackEvents(true).
		DebugEventsUntilDate(ldtime.UnixMillisecondTime(9000)).
		Build()
	_, _ = sharedtest.UpsertFlag(store, flag)

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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, fe.Version)
	assert.Equal(t, ldvalue.String("b"), fe.Value)
	assert.Equal(t, 1, fe.Variation)
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.True(t, fe.TrackEvents) // set by translateEvent from flag.TrackEvents
	assert.Equal(t, flag.DebugEventsUntilDate, fe.DebugEventsUntilDate)
	assert.Equal(t, ldreason.EvalReasonFallthrough, fe.Reason.GetKind())
}

func TestTranslateFeatureEventWithSchemaVersion2AndExistingFlagWithTrackEventsInEvent(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
	}
	flag := ldbuilders.NewFlagBuilder("flagkey").
		Version(22).                                          // deliberately different version from event - we should use the version from the event
		Variations(ldvalue.String("a"), ldvalue.String("x")). // deliberately doesn't include "b" - we want to see that it uses the variation index from the event
		Build()
	_, _ = sharedtest.UpsertFlag(store, flag)

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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, fe.Version)
	assert.Equal(t, ldvalue.String("b"), fe.Value)
	assert.Equal(t, 1, fe.Variation)
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.True(t, fe.TrackEvents) // comes from the event, not the flag
	assert.Equal(t, ldtime.UnixMillisecondTime(0), fe.DebugEventsUntilDate)
	assert.Equal(t, ldreason.EvalReasonFallthrough, fe.Reason.GetKind())
}

func TestTranslateFeatureEventWithSchemaVersion2AndExistingFlagWithDebugEventsUntilDateInEvent(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
	}
	flag := ldbuilders.NewFlagBuilder("flagkey").
		Version(22).                                          // deliberately different version from event - we should use the version from the event
		Variations(ldvalue.String("a"), ldvalue.String("x")). // deliberately doesn't include "b" - we want to see that it uses the variation index from the event
		Build()
	_, _ = sharedtest.UpsertFlag(store, flag)

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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, flag.Key, fe.Key)
	assert.Equal(t, 11, fe.Version)
	assert.Equal(t, ldvalue.String("b"), fe.Value)
	assert.Equal(t, 1, fe.Variation)
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Equal(t, ldtime.UnixMillisecondTime(9000), fe.DebugEventsUntilDate)
	assert.Equal(t, ldreason.EvalReasonFallthrough, fe.Reason.GetKind())
}

func TestTranslateFeatureEventWithSchemaVersion2AndUnknownFlag(t *testing.T) {
	store := sharedtest.NewInMemoryStore()
	er := &eventSummarizingRelay{
		storeAdapter: relaystore.NewSSERelayDataStoreAdapterWithExistingStore(store),
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
	fe := eventOut.(ldevents.FeatureRequestEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), fe.CreationDate)
	assert.Equal(t, "flagkey", fe.Key)
	assert.Equal(t, ldevents.NoVersion, fe.Version)
	assert.Equal(t, ldvalue.String("c"), fe.Value)
	assert.Equal(t, ldevents.NoVariation, fe.Variation)
	assert.Equal(t, ldvalue.String("c"), fe.Default)
	assert.False(t, fe.TrackEvents)
	assert.Equal(t, ldtime.UnixMillisecondTime(0), fe.DebugEventsUntilDate)
	assert.Equal(t, ldreason.EvalReasonError, fe.Reason.GetKind())
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
	ie := eventOut.(ldevents.IdentifyEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), ie.CreationDate)
	assert.Equal(t, "userkey", ie.User.GetKey())
	assert.Equal(t, "Mina", ie.User.GetName().StringValue())
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
	ce := eventOut.(ldevents.CustomEvent)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), ce.CreationDate)
	assert.Equal(t, "customkey", ce.Key)
	assert.Equal(t, "userkey", ce.User.GetKey())
	assert.Equal(t, "Lucy", ce.User.GetName().StringValue())
	assert.Equal(t, ldvalue.String("x"), ce.Data)
	assert.True(t, ce.HasMetric)
	assert.Equal(t, 1.5, ce.MetricValue)
}
