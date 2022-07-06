package oldevents

import (
	"errors"
	"testing"

	st "github.com/launchdarkly/ld-relay/v6/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ldevents "github.com/launchdarkly/go-sdk-events/v2"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const userJSON = `{"key": "userkey", "privateAttrs": ["name"]}`

var expectedEventContext = ldevents.PreserializedContext(ldcontext.New("userkey"),
	[]byte(userJSON))

func requireParsedEvent(t *testing.T, data string, expectedType interface{}) OldEvent {
	e, err := UnmarshalEvent([]byte(data))
	require.NoError(t, err)
	require.IsType(t, expectedType, e)
	return e
}

func TestTranslateFeatureEvent(t *testing.T) {
	flagKey := "flagkey"
	expectedVersion := ldvalue.NewOptionalInt(11)
	expectedVariation := ldvalue.NewOptionalInt(1)
	expectedValue := ldvalue.String("b")
	expectedDefault := ldvalue.String("c")
	baseProps := ldevents.BaseEvent{CreationDate: ldtime.UnixMillisecondTime(1000), Context: expectedEventContext}
	baseJSON := `"kind": "feature", "creationDate": 1000, "key": "flagkey", "user": ` + userJSON +
		`, "value": "b", "default": "c"`
	flagVariations := []ldvalue.Value{ldvalue.String("a"), ldvalue.String("b")}
	debugTime := ldtime.UnixMillisecondTime(1000)

	basicFlag := ldbuilders.NewFlagBuilder(flagKey).Variations(flagVariations...).Build()
	flagWithTracking := ldbuilders.NewFlagBuilder(flagKey).Variations(flagVariations...).TrackEvents(true).Build()
	flagWithDebugging := ldbuilders.NewFlagBuilder(flagKey).Variations(flagVariations...).
		DebugEventsUntilDate(debugTime).Build()

	for _, p := range []struct {
		schemaVersion int
		input         string
		flag          ldmodel.FeatureFlag
		expected      ldevents.EvaluationData
	}{
		{
			schemaVersion: 1,
			input:         `{` + baseJSON + `, "version": 11}`,
			flag:          basicFlag,
			expected: ldevents.EvaluationData{
				BaseEvent: baseProps,
				Key:       "flagkey",
				Version:   expectedVersion,
				Variation: expectedVariation,
				Value:     expectedValue,
				Default:   expectedDefault,
			},
		},
		{
			schemaVersion: 1,
			input:         `{` + baseJSON + `, "version": 11}`,
			flag:          flagWithTracking,
			expected: ldevents.EvaluationData{
				BaseEvent:        baseProps,
				Key:              "flagkey",
				Version:          expectedVersion,
				Variation:        expectedVariation,
				Value:            expectedValue,
				Default:          expectedDefault,
				RequireFullEvent: true,
			},
		},
		{
			schemaVersion: 1,
			input:         `{` + baseJSON + `, "version": 11}`,
			flag:          flagWithDebugging,
			expected: ldevents.EvaluationData{
				BaseEvent:            baseProps,
				Key:                  "flagkey",
				Version:              expectedVersion,
				Variation:            expectedVariation,
				Value:                expectedValue,
				Default:              expectedDefault,
				DebugEventsUntilDate: debugTime,
			},
		},
		{
			schemaVersion: 1,
			input:         `{` + baseJSON + `}`,
			// no flag data, because the lack of "version" means it's an unknown flag
			expected: ldevents.EvaluationData{
				BaseEvent: baseProps,
				Key:       "flagkey",
				Value:     expectedValue,
				Default:   expectedDefault,
			},
		},
		{
			schemaVersion: 2,
			input:         `{` + baseJSON + `, "version": 11, "variation": 1}`,
			flag:          basicFlag,
			expected: ldevents.EvaluationData{
				BaseEvent: baseProps,
				Key:       "flagkey",
				Version:   expectedVersion,
				Variation: expectedVariation,
				Value:     expectedValue,
				Default:   expectedDefault,
			},
		},
		{
			schemaVersion: 2,
			input:         `{` + baseJSON + `, "version": 11, "variation": 1, "trackEvents": false}`,
			// don't need flag data in this case because we see trackEvents
			expected: ldevents.EvaluationData{
				BaseEvent: baseProps,
				Key:       "flagkey",
				Version:   expectedVersion,
				Variation: expectedVariation,
				Value:     expectedValue,
				Default:   expectedDefault,
			},
		},
		{
			schemaVersion: 2,
			input:         `{` + baseJSON + `, "version": 11, "variation": 1, "trackEvents": true}`,
			// don't need flag data in this case because we see trackEvents
			expected: ldevents.EvaluationData{
				BaseEvent:        baseProps,
				Key:              "flagkey",
				Version:          expectedVersion,
				Variation:        expectedVariation,
				Value:            expectedValue,
				Default:          expectedDefault,
				RequireFullEvent: true,
			},
		},
		{
			schemaVersion: 2,
			input:         `{` + baseJSON + `, "version": 11, "variation": 1, "debugEventsUntilDate": 1000}`,
			// don't need flag data in this case because we see debugEventsUntilDate
			expected: ldevents.EvaluationData{
				BaseEvent:            baseProps,
				Key:                  "flagkey",
				Version:              expectedVersion,
				Variation:            expectedVariation,
				Value:                expectedValue,
				Default:              expectedDefault,
				DebugEventsUntilDate: debugTime,
			},
		},
		{
			schemaVersion: 2,
			input:         `{` + baseJSON + `}`,
			// no flag data, because the lack of "version" means it's an unknown flag
			expected: ldevents.EvaluationData{
				BaseEvent: baseProps,
				Key:       "flagkey",
				Value:     expectedValue,
				Default:   expectedDefault,
			},
		},
	} {
		t.Run(p.input, func(t *testing.T) {
			fe := requireParsedEvent(t, p.input, FeatureEvent{}).(FeatureEvent)

			var dataStore subsystems.DataStore
			if p.flag.Key != "" {
				dataStore = mockDataStore{flag: &p.flag}
			}

			data, err := TranslateFeatureEvent(fe, p.schemaVersion, dataStore)
			require.NoError(t, err)
			assert.Equal(t, p.expected, data)
		})
	}
}

func TestTranslateFeatureEventFailsFromDataStoreProblems(t *testing.T) {
	baseJSON := `"kind": "feature", "creationDate": 1000, "key": "flagkey", "user": ` + userJSON +
		`, "value": "b", "default": "c"`

	type params struct {
		schemaVersion int
		input         string
	}
	allParams := []params{
		{
			schemaVersion: 1,
			input:         `{` + baseJSON + `, "version": 11}`,
		},
		{
			schemaVersion: 2,
			input:         `{` + baseJSON + `, "version": 11, "variation": 1}`,
		},
	}

	t.Run("we need flag data but the store is unavailable", func(t *testing.T) {
		for _, p := range allParams {
			t.Run(p.input, func(t *testing.T) {
				fe := requireParsedEvent(t, p.input, FeatureEvent{}).(FeatureEvent)

				_, err := TranslateFeatureEvent(fe, p.schemaVersion, nil)
				assert.Error(t, err)
			})
		}
	})

	t.Run("we need flag data but the store returns an error", func(t *testing.T) {
		for _, p := range allParams {
			t.Run(p.input, func(t *testing.T) {
				fe := requireParsedEvent(t, p.input, FeatureEvent{}).(FeatureEvent)

				fakeError := errors.New("sorry")
				store := mockDataStore{err: fakeError}

				_, err := TranslateFeatureEvent(fe, p.schemaVersion, store)
				assert.Equal(t, fakeError, err)
			})
		}
	})
}

func TestTranslateIdentifyEvent(t *testing.T) {
	ie := requireParsedEvent(t,
		`{
			"kind": "identify",
			"creationDate": 1000,
			"key": "userkey",
			"user": `+userJSON+`
		}`, IdentifyEvent{}).(IdentifyEvent)
	data, err := TranslateIdentifyEvent(ie)
	require.NoError(t, err)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), data.CreationDate)
	assert.Equal(t, expectedEventContext, data.Context)
}

func TestTranslateCustomEvent(t *testing.T) {
	ce1 := requireParsedEvent(t,
		`{
			"kind": "custom",
			"creationDate": 1000,
			"key": "eventkey",
			"user": `+userJSON+`
		}`, CustomEvent{}).(CustomEvent)
	data1, err := TranslateCustomEvent(ce1)
	require.NoError(t, err)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), data1.CreationDate)
	assert.Equal(t, "eventkey", data1.Key)
	assert.Equal(t, expectedEventContext, data1.Context)
	assert.Equal(t, ldvalue.Null(), data1.Data)
	assert.False(t, data1.HasMetric)

	ce2 := requireParsedEvent(t,
		`{
			"kind": "custom",
			"creationDate": 1000,
			"key": "eventkey",
			"user": `+userJSON+`,
			"data": "hi"
		}`, CustomEvent{}).(CustomEvent)
	data2, err := TranslateCustomEvent(ce2)
	require.NoError(t, err)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), data2.CreationDate)
	assert.Equal(t, "eventkey", data2.Key)
	assert.Equal(t, expectedEventContext, data2.Context)
	assert.Equal(t, ldvalue.String("hi"), data2.Data)
	assert.False(t, data2.HasMetric)

	ce3 := requireParsedEvent(t,
		`{
			"kind": "custom",
			"creationDate": 1000,
			"key": "eventkey",
			"user": `+userJSON+`,
			"data": "hi",
			"metricValue": 1.5
		}`, CustomEvent{}).(CustomEvent)
	data3, err := TranslateCustomEvent(ce3)
	require.NoError(t, err)
	assert.Equal(t, ldtime.UnixMillisecondTime(1000), data3.CreationDate)
	assert.Equal(t, "eventkey", data3.Key)
	assert.Equal(t, expectedEventContext, data3.Context)
	assert.Equal(t, ldvalue.String("hi"), data3.Data)
	assert.True(t, data3.HasMetric)
	assert.Equal(t, float64(1.5), data3.MetricValue)
}

type mockDataStore struct {
	flag *ldmodel.FeatureFlag
	err  error
}

func (s mockDataStore) Init([]ldstoretypes.Collection) error {
	panic("should not be called in tests")
}

func (s mockDataStore) Get(kind ldstoretypes.DataKind, key string) (ldstoretypes.ItemDescriptor, error) {
	if s.err != nil {
		return ldstoretypes.ItemDescriptor{}, s.err
	}
	return st.FlagDesc(*s.flag), nil
}

func (s mockDataStore) GetAll(kind ldstoretypes.DataKind) ([]ldstoretypes.KeyedItemDescriptor, error) {
	panic("should not be called in tests")
}

func (s mockDataStore) Upsert(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) (bool, error) {
	panic("should not be called in tests")
}

func (s mockDataStore) IsInitialized() bool {
	panic("should not be called in tests")
}

func (s mockDataStore) IsStatusMonitoringEnabled() bool {
	panic("should not be called in tests")
}

func (s mockDataStore) Close() error {
	panic("should not be called in tests")
}
