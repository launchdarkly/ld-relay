package events

const (
	summarizableFeatureEvents = `
[
	{
		"kind": "feature", "creationDate": 1000,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"value": "b", "default": "c"
	},
	{
		"kind": "feature", "creationDate": 1001,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"value": "b", "default": "c"
	}
]`

	summarizableFeatureEventsWithoutVersion = `
[
	{
		"kind": "feature", "creationDate": 1000,
		"key": "flagkey",
		"user": { "key": "userkey" },
		"value": "b", "default": "c"
	},
	{
		"kind": "feature", "creationDate": 1001,
		"key": "flagkey",
		"user": { "key": "userkey" },
		"value": "b", "default": "c"
	}
]`

	summarizableFeatureEventsSchemaV2 = `
[
	{
		"kind": "feature", "creationDate": 1000,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "feature", "creationDate": 1001,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	}
]`

	summarizableFeatureEventsSchemaV2WithoutVersion = `
[
	{
		"kind": "feature", "creationDate": 1000,
		"key": "flagkey",
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "feature", "creationDate": 1001,
		"key": "flagkey",
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	}
]`

	summarizableFeatureEventsSchemaV2TrackEvents = `
[
	{
		"kind": "feature", "creationDate": 1000,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c",
		"trackEvents": true
	},
	{
		"kind": "feature", "creationDate": 1001,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c",
		"trackEvents": true
	}
]`

	summarizableFeatureEventsSchemaV2DebugEvents = `
[
	{
		"kind": "feature", "creationDate": 1000,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c",
		"debugEventsUntilDate": 9999999999999
	},
	{
		"kind": "feature", "creationDate": 1001,
		"key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c",
		"debugEventsUntilDate": 9999999999999
	}
]`

	expectedSummarizedFeatureEventsOutput = `
[
	{
		"kind": "index", "creationDate": 1000, "user": { "key": "userkey" }
	},
	{
		"kind": "summary", "startDate": 1000, "endDate": 1001,
		"features": {
			"flagkey": {
				"default": "c",
				"counters": [ { "variation": 1, "version": 11, "value": "b", "count": 2 } ]
			}
		}
	}
]`

	expectedSummarizedFeatureEventsOutputTrackEvents = `
[
	{
		"kind": "index", "creationDate": 1000, "user": { "key": "userkey" }
	},
	{
		"kind": "feature", "creationDate": 1000, "key": "flagkey", "version": 11,
		"userKey": "userkey",
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "feature", "creationDate": 1001, "key": "flagkey", "version": 11,
		"userKey": "userkey",
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "summary", "startDate": 1000, "endDate": 1001,
		"features": {
			"flagkey": {
				"default": "c",
				"counters": [ { "variation": 1, "version": 11, "value": "b", "count": 2 } ]
			}
		}
	}
]`

	expectedSummarizedFeatureEventsOutputTrackEventsInlineUsers = `
[
	{
		"kind": "feature", "creationDate": 1000, "key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "feature", "creationDate": 1001, "key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "summary", "startDate": 1000, "endDate": 1001,
		"features": {
			"flagkey": {
				"default": "c",
				"counters": [ { "variation": 1, "version": 11, "value": "b", "count": 2 } ]
			}
		}
	}
]`

	expectedSummarizedFeatureEventsOutputDebugEvents = `
[
	{
		"kind": "index", "creationDate": 1000, "user": { "key": "userkey" }
	},
	{
		"kind": "debug", "creationDate": 1000, "key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "debug", "creationDate": 1001, "key": "flagkey", "version": 11,
		"user": { "key": "userkey" },
		"variation": 1, "value": "b", "default": "c"
	},
	{
		"kind": "summary", "startDate": 1000, "endDate": 1001,
		"features": {
			"flagkey": {
				"default": "c",
				"counters": [ { "variation": 1, "version": 11, "value": "b", "count": 2 } ]
			}
		}
	}
]`

	expectedSummarizedFeatureEventsOutputUnknownFlagWithoutVersion = `
[
	{
		"kind": "index", "creationDate": 1000, "user": { "key": "userkey" }
	},
	{
		"kind": "summary", "startDate": 1000, "endDate": 1001,
		"features": {
			"flagkey": {
				"default": "c",
				"counters": [ { "unknown": true, "value": "b", "count": 2 } ]
			}
		}
	}
]`

	expectedSummarizedFeatureEventsOutputUnknownFlagWithVersion = `
[
	{
		"kind": "index", "creationDate": 1000, "user": { "key": "userkey" }
	},
	{
		"kind": "summary", "startDate": 1000, "endDate": 1001,
		"features": {
			"flagkey": {
				"default": "c",
				"counters": [ { "version": 11, "value": "b", "count": 2 } ]
			}
		}
	}
]`

	summarizableCustomEvents = `
[
	{
		"kind": "custom", "creationDate": 1000, "key": "eventkey1", "user": { "key": "userkey" }
	},
	{
		"kind": "custom", "creationDate": 1001, "key": "eventkey2", "user": { "key": "userkey" },
		"data": "eventdata", "metricValue": 1.5
	}
]`

	expectedSummarizedCustomEvents = `
[
	{
		"kind": "index", "creationDate": 1000, "user": { "key": "userkey" }
	},
	{
		"kind": "custom", "creationDate": 1000, "key": "eventkey1", "userKey": "userkey"
	},
	{
		"kind": "custom", "creationDate": 1001, "key": "eventkey2", "userKey": "userkey",
		"data": "eventdata", "metricValue": 1.5
	}
]`

	identifyEvents = `
[
	{
		"kind": "identify", "creationDate": 1000, "key": "userkey1", "user": { "key": "userkey1" }
	},
	{
		"kind": "identify", "creationDate": 1001, "key": "userkey2", "user": { "key": "userkey2" }
	}
]`

	malformedEventsAndGoodIdentifyEventsInWellFormedJSON = `
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
]`
)
