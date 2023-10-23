package events

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
)

// Things to know about this test data:
//
// - The summarizing logic deliberately does not do any transformation of the JSON user properties
// from the original event, since the PHP SDK has already handled private attribute redaction. So,
// the "context" property in the transformed event will have an old-style user object in it. Those
// are still allowed in the new schema.
// - We're not trying to cover all the possible behaviors of the event processor here with regard
// to summary event output, context deduplication, etc. Those are already covered by the unit tests
// in go-sdk-events. It's enough for us to verify that 1. we're actually using the EventProcessor
// and sending its output, and 2. we are correctly building the inputs for the EventProcessor
// methods that we're calling. So the test cases here only need to cover the possible branches for
// the decisions we're making, like "what kind of event is this" and "do we need to compute any
// properties that weren't provided in the event, from flag data".

type summarizeEventsParams struct {
	name               string
	schemaVersion      int
	storedFlag         ldmodel.FeatureFlag
	inputEventsJSON    string
	expectedEventsJSON string
}

func makeTestFlag(trackEvents bool, debugEventsUntilDate ldtime.UnixMillisecondTime) ldmodel.FeatureFlag {
	return ldbuilders.NewFlagBuilder("flagkey").
		Version(22). // deliberately different version from the event data - we should use the version from the event
		Variations(ldvalue.String("a"), ldvalue.String("b")).
		TrackEvents(trackEvents).
		DebugEventsUntilDate(debugEventsUntilDate).
		Build()
}

func makeBasicSummarizeEventsParams() summarizeEventsParams {
	// This is just the minimal test case to verify that the summarizer is being used at all.
	return summarizeEventsParams{
		name: "basic summarizer test",
		inputEventsJSON: `
			[
				{
					"kind": "feature", "creationDate": 1000, "key": "flagkey", "version": 11,
					"user": { "key": "userkey" }, "variation": 1, "value": "b", "default": "c"
				}
			]`,
		expectedEventsJSON: `
			[
				{
					"kind": "index", "creationDate": 1000, "context": {"key": "userkey"}
				},
				{
					"kind": "summary", "startDate": 1000, "endDate": 1000,
					"features": {
						"flagkey": {
							"default": "c", "contextKinds": [ "user" ],
							"counters": [ { "variation": 1, "version": 11, "value": "b", "count": 1 } ]
						}
					}
				}
			]`,
	}
}

func makeAllSummarizeEventsParams() []summarizeEventsParams {
	return []summarizeEventsParams{
		{
			name: "feature event that does not require reading flag data",
			inputEventsJSON: `
			[
				{ "kind": "feature", "creationDate": 1000, "key": "flagkey", "user": {"key": "userkey"},
				"value": "b", "default": "c", "version": 11, "variation": 1, "trackEvents": false, "samplingRatio": 1, "excludeFromSummaries": false }
			]`,
			expectedEventsJSON: `
			[
				{ "kind": "index", "creationDate": 1000, "context": {"key": "userkey"} },
				{
					"kind": "summary", "startDate": 1000, "endDate": 1000,
					"features": {
						"flagkey": {
							"default": "c", "contextKinds": ["user"],
							"counters": [{"variation": 1, "version": 11, "value": "b", "count": 1}]
						}
					}
				}
			]`,
		},
		{
			name: "feature event has non-standard defaults for ratio and exclusion fields",
			inputEventsJSON: `
			[
				{ "kind": "feature", "creationDate": 1000, "key": "flagkey", "user": {"key": "userkey"},
				"value": "b", "default": "c", "version": 11, "variation": 1, "trackEvents": false, "samplingRatio": 2, "excludeFromSummaries": true }
			]`,
			expectedEventsJSON: `
			[
				{ "kind": "index", "creationDate": 1000, "context": {"key": "userkey"} }
			]`,
		},
		{
			name: "feature event includes the provided sampling ratio",
			inputEventsJSON: `
			[
				{ "kind": "feature", "creationDate": 1000, "key": "flagkey", "user": {"key": "userkey"},
				"value": "b", "default": "c", "version": 11, "variation": 1, "trackEvents": true, "samplingRatio": 0 }
			]`,
			expectedEventsJSON: `
			[
				{"kind":"index","creationDate":1000,"context":{"key": "userkey"}},
				{"kind":"feature","creationDate":1000,"key":"flagkey","version":11,"contextKeys":{"user":"userkey"},"variation":1,"value":"b","default":"c","samplingRatio":0},
				{"kind":"summary","startDate":1000,"endDate":1000,"features":{"flagkey":{"default":"c","counters":[{"variation":1,"version":11,"value":"b","count":1}],"contextKinds":["user"]}}}
			]`,
		},
		{
			name: "feature event can be emitted without the summary event",
			inputEventsJSON: `
			[
				{ "kind": "feature", "creationDate": 1000, "key": "flagkey", "user": {"key": "userkey"},
				"value": "b", "default": "c", "version": 11, "variation": 1, "trackEvents": true, "samplingRatio": 1, "excludeFromSummaries": true }
			]`,
			expectedEventsJSON: `
			[
				{"kind":"index","creationDate":1000,"context":{"key": "userkey"}},
				{"kind":"feature","creationDate":1000,"key":"flagkey","version":11,"contextKeys":{"user":"userkey"},"variation":1,"value":"b","default":"c"}
			]`,
		},
		{
			name: "feature event that requires reading flag data",
			storedFlag: ldbuilders.NewFlagBuilder("flagkey").Version(11).
				Variations(ldvalue.String("a"), ldvalue.String("b")).Build(),
			inputEventsJSON: `
			[
				{ "kind": "feature", "creationDate": 1000, "key": "flagkey", "user": {"key": "userkey"},
				  "value": "b", "default": "c", "version": 11 }
			]`, // "variation" will be derived from the stored flag
			expectedEventsJSON: `
			[
				{ "kind": "index", "creationDate": 1000, "context": {"key": "userkey"} },
				{
					"kind": "summary", "startDate": 1000, "endDate": 1000,
					"features": {
						"flagkey": {
							"default": "c", "contextKinds": ["user"],
							"counters": [{"variation": 1, "version": 11, "value": "b", "count": 1}]
						}
					}
				}
			]`,
		},
		{
			name: "custom event",
			inputEventsJSON: `
			[
				{ "kind": "custom", "creationDate": 1000, "key": "eventkey1", "user": {"key": "userkey"} }
			]`,
			expectedEventsJSON: `
			[
				{ "kind": "index", "creationDate": 1000, "context": {"key": "userkey"} },
				{ "kind": "custom", "creationDate": 1000, "key": "eventkey1", "contextKeys": {"user": "userkey"} }
			]`,
		},
		{
			name: "identify event",
			inputEventsJSON: `
			[
				{ "kind": "identify", "creationDate": 1000, "key": "userkey1", "user": {"key": "userkey"} }
			]`,
			expectedEventsJSON: `
			[
				{ "kind": "identify", "creationDate": 1000, "context": {"key": "userkey"} }
			]`,
		},
		{
			name: "alias event",
			inputEventsJSON: `
			[
				{
					"kind": "alias", "creationDate": 1000, "key": "userkey1", "contextKind": "user",
					"previousKey": "anonkey", "previousContextKind": "anonymous"
				}
			]`,
			expectedEventsJSON: `
			[
				{
					"kind": "alias", "creationDate": 1000, "key": "userkey1", "contextKind": "user",
					"previousKey": "anonkey", "previousContextKind": "anonymous"
				}
			]`, // unchanged
		},
		{
			name: "unparseable events are removed",
			inputEventsJSON: `
			[
				{
					"kind": "feature", "creationDate": "not a number",
					"key": "flagkey", "version": 11,
					"user": { "key": "userkey" },
					"value": "b", "default": "c"
				},
				{
					"kind": "identify", "creationDate": "not a number", "key": "userkey1", "user": { "key": "userkey1" }
				},
				{
					"kind": "custom", "creationDate": "not a number", "key": "eventkey1", "user": { "key": "userkey" }
				},
				{
					"kind": "unknown-event-kind"
				},
				{
					"kind": false
				},
				{
					"kind": "identify", "creationDate": 1000, "key": "userkey1", "user": { "key": "userkey1" }
				},
				{
					"kind": "identify", "creationDate": 1001, "key": "userkey2", "user": { "key": "userkey2" }
				}
			]`,
			expectedEventsJSON: `
			[
				{
					"kind": "unknown-event-kind"
				},
				{
					"kind": "identify", "creationDate": 1000, "context": { "key": "userkey1" }
				},
				{
					"kind": "identify", "creationDate": 1001, "context": { "key": "userkey2" }
				}
			]`,
		},
	}
}
