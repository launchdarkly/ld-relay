package oldevents

import (
	"encoding/json"
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldreason"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeTime = ldtime.UnixMillisecondTime(1000)

func TestUnmarshalFeatureEvent(t *testing.T) {
	t.Run("minimal properties", func(t *testing.T) {
		withUserOrContext(t, func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext) {
			eventJSON := `{
	"kind": "feature",
	"creationDate": 1000,
	"key": "flagkey",` +
				contextPropStr + `,
	"value": "v"
}`
			e, err := UnmarshalEvent([]byte(eventJSON))
			require.NoError(t, err)
			require.IsType(t, FeatureEvent{}, e)
			fe := e.(FeatureEvent)
			assert.Equal(t, featureKind, fe.Kind())
			assert.Equal(t, fakeTime, fe.CreationDate)
			assert.Equal(t, "flagkey", fe.Key)
			assert.Equal(t, ldvalue.OptionalInt{}, fe.Version)
			assert.Equal(t, expectedContext, fe.actualContext)
			assert.Equal(t, ldvalue.String("v"), fe.Value)
			assert.Equal(t, ldvalue.OptionalInt{}, fe.Variation)
			assert.Equal(t, ldreason.EvaluationReason{}, fe.Reason)
			assert.Equal(t, ldvalue.OptionalBool{}, fe.TrackEvents)
			assert.Equal(t, ldtime.UnixMillisecondTime(0), fe.DebugEventsUntilDate)
		})
	})

	t.Run("all properties", func(t *testing.T) {
		withUserOrContext(t, func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext) {
			eventJSON := `{
	"kind": "feature",
	"creationDate": 1000,
	"key": "flagkey",
	"version": 1,` +
				contextPropStr + `,
	"value": "v",
	"variation": 2,
	"reason": {"kind": "OFF"},
	"trackEvents": true,
	"debugEventsUntilDate": 1001
}`
			e, err := UnmarshalEvent([]byte(eventJSON))
			require.NoError(t, err)
			require.IsType(t, FeatureEvent{}, e)
			fe := e.(FeatureEvent)
			assert.Equal(t, fakeTime, fe.CreationDate)
			assert.Equal(t, "flagkey", fe.Key)
			assert.Equal(t, ldvalue.NewOptionalInt(1), fe.Version)
			assert.Equal(t, expectedContext, fe.actualContext)
			assert.Equal(t, ldvalue.String("v"), fe.Value)
			assert.Equal(t, ldvalue.NewOptionalInt(2), fe.Variation)
			assert.Equal(t, ldreason.NewEvalReasonOff(), fe.Reason)
			assert.Equal(t, ldvalue.NewOptionalBool(true), fe.TrackEvents)
			assert.Equal(t, ldtime.UnixMillisecondTime(1001), fe.DebugEventsUntilDate)
		})
	})

	t.Run("no user or context", func(t *testing.T) {
		eventJSON := `{
			"kind": "feature",
			"creationDate": 1000,
			"key": "flagkey",
			"version": 1,
			"value": "v"
		}`
		_, err := UnmarshalEvent([]byte(eventJSON))
		assert.Equal(t, errEventHadNoUserOrContext, err)
	})
}

func TestUnmarshalIdentifyEvent(t *testing.T) {
	withUserOrContext(t, func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext) {
		eventJSON := `{
	"kind": "identify",
	"creationDate": 1000,
	"key": "z",` +
			contextPropStr + `
}`
		e, err := UnmarshalEvent([]byte(eventJSON))
		require.NoError(t, err)
		require.IsType(t, IdentifyEvent{}, e)
		ie := e.(IdentifyEvent)
		assert.Equal(t, identifyKind, ie.Kind())
		assert.Equal(t, fakeTime, ie.CreationDate)
		assert.Equal(t, expectedContext, ie.actualContext)
	})

	t.Run("no user or context", func(t *testing.T) {
		eventJSON := `{
			"kind": "identify",
			"creationDate": 1000
		}`
		_, err := UnmarshalEvent([]byte(eventJSON))
		assert.Equal(t, errEventHadNoUserOrContext, err)
	})
}

func TestUnmarshalCustomEvent(t *testing.T) {
	t.Run("minimal properties", func(t *testing.T) {
		withUserOrContext(t, func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext) {
			eventJSON := `{
	"kind": "custom",
	"creationDate": 1000,
	"key": "eventkey",` +
				contextPropStr + `
}`
			e, err := UnmarshalEvent([]byte(eventJSON))
			require.NoError(t, err)
			require.IsType(t, CustomEvent{}, e)
			ce := e.(CustomEvent)
			assert.Equal(t, customKind, ce.Kind())
			assert.Equal(t, fakeTime, ce.CreationDate)
			assert.Equal(t, "eventkey", ce.Key)
			assert.Equal(t, expectedContext, ce.actualContext)
			assert.Equal(t, ldvalue.Null(), ce.Data)
			assert.Nil(t, ce.MetricValue)
		})
	})

	t.Run("all properties", func(t *testing.T) {
		withUserOrContext(t, func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext) {
			eventJSON := `{
	"kind": "custom",
	"creationDate": 1000,
	"key": "eventkey",` +
				contextPropStr + `,
	"data": "x",
	"metricValue": 1.5
}`
			e, err := UnmarshalEvent([]byte(eventJSON))
			require.NoError(t, err)
			require.IsType(t, CustomEvent{}, e)
			ce := e.(CustomEvent)
			assert.Equal(t, fakeTime, ce.CreationDate)
			assert.Equal(t, "eventkey", ce.Key)
			assert.Equal(t, expectedContext, ce.actualContext)
			assert.Equal(t, ldvalue.String("x"), ce.Data)
			if assert.NotNil(t, ce.MetricValue) {
				assert.Equal(t, float64(1.5), *ce.MetricValue)
			}
		})
	})

	t.Run("no user or context", func(t *testing.T) {
		eventJSON := `{
			"kind": "custom",
			"creationDate": 1000,
			"key": "eventkey"
		}`
		_, err := UnmarshalEvent([]byte(eventJSON))
		assert.Equal(t, errEventHadNoUserOrContext, err)
	})
}

func TestUnmarshalOtherEvent(t *testing.T) {
	withUserOrContext(t, func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext) {
		eventJSON := `{
	"kind": "alias",
	"creationDate": 1000,
	"key": "key1",
	"whatever": "we're not even going to parse these properties"
}`
		e, err := UnmarshalEvent([]byte(eventJSON))
		require.NoError(t, err)
		assert.Equal(t, "alias", e.Kind())
		require.IsType(t, UntranslatedEvent{}, e)
		ue := e.(UntranslatedEvent)
		assert.Equal(t, eventJSON, string(ue.RawEvent))
	})
}

func withUserOrContext(t *testing.T, action func(t *testing.T, contextPropStr string, expectedContext ldevents.EventInputContext)) {
	doTest := func(t *testing.T, propName, propValue string, c ldcontext.Context) {
		action(t, `"`+propName+`": `+propValue, ldevents.PreserializedContext(c, []byte(propValue)))
	}

	t.Run("with user", func(t *testing.T) {
		doTest(t, "user", `{"key": "a", "name": "b"}`,
			ldcontext.NewBuilder("a").Build())
	})

	t.Run("with user with privateAttrs", func(t *testing.T) {
		doTest(t, "user", `{"key": "a", "name": "b", "privateAttrs": ["email"]}`,
			ldcontext.NewBuilder("a").Build())
	})

	t.Run("with basic context", func(t *testing.T) {
		doTest(t, "context", `{"kind": "x", "key": "a", "name": "b"}`,
			ldcontext.NewWithKind("x", "a"))
	})

	t.Run("with basic context wth redactedAttributes", func(t *testing.T) {
		doTest(t, "context", `{"kind": "x", "key": "a", "name": "b", "_meta": {"redactedAttributes": ["email"]}}`,
			ldcontext.NewWithKind("x", "a"))
	})

	t.Run("with multi-kind context", func(t *testing.T) {
		doTest(t, "context", `{"kind": "multi", "kind1": {"key": "a"}, "kind2": {"key": "b"}}`,
			ldcontext.NewMulti(ldcontext.NewWithKind("kind1", "a"), ldcontext.NewWithKind("kind2", "b")))
	})
}

func TestUnmarshalEventContextValidation(t *testing.T) {
	for _, validUser := range []string{
		`{"key": ""}`,
	} {
		t.Run(validUser, func(t *testing.T) {
			var c ldcontext.Context
			require.NoError(t, json.Unmarshal([]byte(validUser), &c))

			e, err := UnmarshalEvent([]byte(`{"kind": "identify", "user": ` + validUser + `}`))
			require.NoError(t, err)
			assert.Equal(t, ldevents.PreserializedContext(c, []byte(validUser)), e.(IdentifyEvent).actualContext)
		})
	}

	for _, invalidUser := range []string{
		`{}`,
		`{"key": true}`,
	} {
		t.Run(invalidUser, func(t *testing.T) {
			_, err := UnmarshalEvent([]byte(`{"kind": "identify", "user": ` + invalidUser + `}`))
			assert.Error(t, err)
		})
	}
}

func TestUnmarshalEventErrors(t *testing.T) {
	for _, s := range []string{
		``,
		`{no`,
		`{}`,
		`{"kind": true}`,
		`{"kind": ""}`,
	} {
		_, err := UnmarshalEvent([]byte(s))
		assert.Error(t, err, "for input: %s", s)
	}
}
