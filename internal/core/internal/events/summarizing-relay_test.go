package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	ldevents "github.com/launchdarkly/go-sdk-events/v2"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	m "github.com/launchdarkly/go-test-helpers/v2/matchers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func expectSummarizedPayload(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) string {
	r := expectSummarizedPayloadRequest(t, requestsCh)
	return string(r.Body)
}

func expectSummarizedPayloadRequest(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) httphelpers.HTTPRequestInfo {
	r := st.ExpectTestRequest(t, requestsCh, time.Second)
	assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
	assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"))
	return r
}

func TestSummarizeEvents(t *testing.T) {
	for _, ep := range makeAllSummarizeEventsParams() {
		t.Run(ep.name, func(t *testing.T) {
			var tryParse interface{}
			if err := json.Unmarshal([]byte(ep.inputEventsJSON), &tryParse); err != nil {
				require.NoError(t, err, "test input was not valid JSON: %s", ep.inputEventsJSON)
			}
			if err := json.Unmarshal([]byte(ep.expectedEventsJSON), &tryParse); err != nil {
				require.NoError(t, err, "test expectation was not valid JSON: %s", ep.expectedEventsJSON)
			}
			eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
				if ep.storedFlag.Key != "" {
					_, _ = st.UpsertFlag(p.dataStore, ep.storedFlag)
				}

				req := st.BuildRequest("POST", "/", []byte(ep.inputEventsJSON), headersWithEventSchema(ep.schemaVersion))
				p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
				p.dispatcher.flush()

				payload := expectSummarizedPayload(t, p.requestsCh)
				m.In(t).Assert(payload, m.JSONStrEqual(ep.expectedEventsJSON))
			})
		})
	}
}

func TestSummarizingRelayProcessesEventsSeparatelyForDifferentTags(t *testing.T) {
	customEventData1a := `{
		"kind": "custom", "creationDate": 1000, "key": "eventkey1a", "user": { "key": "userkey" }
	}`
	customEventData1b := `{
		"kind": "custom", "creationDate": 1001, "key": "eventkey1b", "user": { "key": "userkey" }
	}`
	customEventData2 := `{
		"kind": "custom", "creationDate": 1001, "key": "eventkey2", "user": { "key": "userkey" }
	}`
	payload1a := `[` + customEventData1a + `]`
	payload1b := `[` + customEventData1b + `]`
	payload2 := `[` + customEventData2 + `]`
	headers1, headers2 := headersWithEventSchema(0), headersWithEventSchema(0)
	headers1.Set(TagsHeader, "tags1")
	headers2.Set(TagsHeader, "tags2")

	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req1a := st.BuildRequest("POST", "/", []byte(payload1a), headers1)
		req1b := st.BuildRequest("POST", "/", []byte(payload1b), headers1)
		req2 := st.BuildRequest("POST", "/", []byte(payload2), headers2)
		for _, req := range []*http.Request{req1a, req2, req1b} {
			p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		}
		p.dispatcher.flush()

		request1 := expectSummarizedPayloadRequest(t, p.requestsCh)
		request2 := expectSummarizedPayloadRequest(t, p.requestsCh)
		if request2.Request.Header.Get(TagsHeader) == "tags1" {
			request1, request2 = request2, request1
		}

		assert.Equal(t, "tags1", request1.Request.Header.Get(TagsHeader))
		assert.Equal(t, "tags2", request2.Request.Header.Get(TagsHeader))

		m.In(t).Assert(json.RawMessage(request1.Body), m.JSONArray().Should(m.ItemsInAnyOrder(
			m.MapIncluding(m.KV("kind", m.Equal("index"))),
			m.MapIncluding(m.KV("kind", m.Equal("custom")), m.KV("key", m.Equal("eventkey1a"))),
			m.MapIncluding(m.KV("kind", m.Equal("custom")), m.KV("key", m.Equal("eventkey1b"))),
		)))
		m.In(t).Assert(json.RawMessage(request2.Body), m.JSONArray().Should(m.ItemsInAnyOrder(
			m.MapIncluding(m.KV("kind", m.Equal("index"))),
			m.MapIncluding(m.KV("kind", m.Equal("custom")), m.KV("key", m.Equal("eventkey2"))),
		)))
	})
}

func TestSummarizingRelayPeriodicallyClosesInactiveEventProcessors(t *testing.T) {
	customEventData1a := `{
		"kind": "custom", "creationDate": 1000, "key": "eventkey1a", "user": { "key": "userkey" }
	}`
	customEventData1b := `{
		"kind": "custom", "creationDate": 1001, "key": "eventkey1b", "user": { "key": "userkey" }
	}`
	customEventData2 := `{
		"kind": "custom", "creationDate": 1001, "key": "eventkey2", "user": { "key": "userkey" }
	}`
	payload1a := `[` + customEventData1a + `]`
	payload1b := `[` + customEventData1b + `]`
	payload2 := `[` + customEventData2 + `]`
	headers1, headers2 := headersWithEventSchema(0), headersWithEventSchema(0)
	headers1.Set(TagsHeader, "tags1")
	headers2.Set(TagsHeader, "tags2")

	// Force eventQueueCleanupInterval to be very brief, so that the EventProcessor instances created
	// for the two tags will be torn down again soon after they stop receiving events.
	options := eventRelayTestOptions{eventQueueCleanupInterval: time.Millisecond * 10}

	eventRelayTestWithOptions(t, st.EnvMain, config.EventsConfig{}, options, func(p eventRelayTestParams) {
		req1a := st.BuildRequest("POST", "/", []byte(payload1a), headers1)
		req2 := st.BuildRequest("POST", "/", []byte(payload2), headers2)
		for _, req := range []*http.Request{req1a, req2} {
			p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		}

		// Don't bother doing an explicit flush here - we expect a flush to happen automatically
		// when the two EventProcessor instances are shut down, so once we get these requests, we
		// know that that has happened.
		request1a := expectSummarizedPayloadRequest(t, p.requestsCh)
		request2 := expectSummarizedPayloadRequest(t, p.requestsCh)
		if request2.Request.Header.Get(TagsHeader) == "tags1" {
			request1a, request2 = request2, request1a
		}

		assert.Equal(t, "tags1", request1a.Request.Header.Get(TagsHeader))
		assert.Equal(t, "tags2", request2.Request.Header.Get(TagsHeader))

		m.In(t).Assert(json.RawMessage(request1a.Body), m.JSONArray().Should(m.ItemsInAnyOrder(
			m.MapIncluding(m.KV("kind", m.Equal("index"))),
			m.MapIncluding(m.KV("kind", m.Equal("custom")), m.KV("key", m.Equal("eventkey1a"))),
		)))
		m.In(t).Assert(json.RawMessage(request2.Body), m.JSONArray().Should(m.ItemsInAnyOrder(
			m.MapIncluding(m.KV("kind", m.Equal("index"))),
			m.MapIncluding(m.KV("kind", m.Equal("custom")), m.KV("key", m.Equal("eventkey2"))),
		)))

		// Now, if we send another request using one of the previously-seen tag values, a new
		// EventProcessor should be created for it automatically.
		req1b := st.BuildRequest("POST", "/", []byte(payload1b), headers1)
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req1b)
		p.dispatcher.flush()

		request1b := expectSummarizedPayloadRequest(t, p.requestsCh)
		assert.Equal(t, "tags1", request1b.Request.Header.Get(TagsHeader))
		m.In(t).Assert(json.RawMessage(request1b.Body), m.JSONArray().Should(m.ItemsInAnyOrder(
			m.MapIncluding(m.KV("kind", m.Equal("index"))),
			m.MapIncluding(m.KV("kind", m.Equal("custom")), m.KV("key", m.Equal("eventkey1b"))),
		)))
	})
}
