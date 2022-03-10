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
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/store"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	m "github.com/launchdarkly/go-test-helpers/v2/matchers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestFlag(trackEvents bool, debugEventsUntilDate ldtime.UnixMillisecondTime) ldmodel.FeatureFlag {
	return ldbuilders.NewFlagBuilder("flagkey").
		Version(22). // deliberately different version from the event data - we should use the version from the event
		Variations(ldvalue.String("a"), ldvalue.String("b")).
		TrackEvents(trackEvents).
		DebugEventsUntilDate(debugEventsUntilDate).
		Build()
}

func makeTestFlagForPHP(trackEvents bool, debugEventsUntilDate ldtime.UnixMillisecondTime) ldmodel.FeatureFlag {
	// Schema version 2 means the events are coming from the PHP SDK, which *does* provide "variation",
	// unlike other old SDKs. To verify that we are using the variation number from the event, instead
	// of trying to infer it from the flag variations, we'll deliberately change the flag variations so
	// none of them match the value in the event.
	flag := makeTestFlag(trackEvents, debugEventsUntilDate)
	flag.Variations = []ldvalue.Value{ldvalue.String("x"), ldvalue.String("y")}
	return flag
}

func makeStoreAdapterWithExistingStore(s interfaces.DataStore) *store.SSERelayDataStoreAdapter {
	a := store.NewSSERelayDataStoreAdapter(st.ExistingDataStoreFactory{Instance: s}, nil)
	_, _ = a.CreateDataStore(st.SDKContextImpl{}, nil) // ensure the wrapped store has been created
	return a
}

func expectSummarizedPayload(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) string {
	r := expectSummarizedPayloadRequest(t, requestsCh)
	return string(r.Body)
}

func expectSummarizedPayloadRequest(t *testing.T, requestsCh <-chan httphelpers.HTTPRequestInfo) httphelpers.HTTPRequestInfo {
	r := st.ExpectTestRequest(t, requestsCh, time.Second)
	assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
	assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"))
	return r
}

func TestSummarizeFeatureEventsForExistingFlag(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlag(false, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutput))
	})
}

func TestSummarizeFeatureEventsForExistingFlagWithTrackEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlag(true, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputTrackEvents))
	})
}

func TestSummarizeFeatureEventsForExistingFlagWithInlineUsersAndInlineUsers(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{InlineUsers: true}, func(p eventRelayTestParams) {
		flag := makeTestFlag(true, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputTrackEventsInlineUsers))
	})
}

func TestSummarizeFeatureEventsForExistingFlagWithDebugEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlag(false, ldtime.UnixMillisNow()+1000000)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputDebugEvents))
	})
}

func TestSummarizeFeatureEventsForUnknownFlagWithoutVersion(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsWithoutVersion), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputUnknownFlagWithoutVersion))
	})
}

func TestSummarizeFeatureEventsForUnknownFlagWithEventVersion(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputUnknownFlagWithVersion))
	})
}

func TestSummarizeSchemaV2FeatureEventsForExistingFlag(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlagForPHP(false, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2), headersWithEventSchema(2))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutput))
	})
}

func TestSummarizeSchemaV2FeatureEventsForExistingFlagWithTrackEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		// Here we're not setting trackEvents in the flag; it's specified by the event from the PHP SDK.
		flag := makeTestFlag(false, 0)
		flag.Variations = []ldvalue.Value{ldvalue.String("x"), ldvalue.String("y")}
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2TrackEvents), headersWithEventSchema(2))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputTrackEvents))
	})
}

func TestSummarizeSchemaV2FeatureEventsForExistingFlagWithDebugEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		// Here we're not setting debugEventsUntilDate in the flag; it's specified by the event from the PHP SDK.
		flag := makeTestFlag(false, 0)
		flag.Variations = []ldvalue.Value{ldvalue.String("x"), ldvalue.String("y")}
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2DebugEvents), headersWithEventSchema(2))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputDebugEvents))
	})
}

func TestSummarizeSchemaV2FeatureEventsForUnknownFlag(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2WithoutVersion), headersWithEventSchema(2))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedFeatureEventsOutputUnknownFlagWithoutVersion))
	})
}

func TestSummarizeCustomEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableCustomEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(expectedSummarizedCustomEvents))
	})
}

func TestSummarizeCustomEventsWithInlineUsersLeavesEventsUnchanged(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{InlineUsers: true}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableCustomEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(summarizableCustomEvents))
	})
}

func TestSummarizeIdentifyEventsLeavesEventsUnchanged(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(identifyEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(identifyEvents))
	})
}

func TestSummarizeAliasEventsLeavesEventsUnchanged(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(aliasEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(aliasEvents))
	})
}

func TestSummarizingRelayDiscardsMalformedEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(malformedEventsAndGoodIdentifyEventsInWellFormedJSON), headersWithEventSchema(0))
		p.dispatcher.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		m.In(t).Assert(payload, m.JSONStrEqual(identifyEvents))
	})
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

func TestCanSeePrivateAttrsOfPHPEventUser(t *testing.T) {
	var ru receivedEventUser
	require.NoError(t, json.Unmarshal([]byte(`{"key": "k", "name": "n", "privateAttrs": ["email"]}`), &ru))
	assert.Equal(t, lduser.NewUserBuilder("k").Name("n").Build(), ru.eventUser.User)
	assert.Equal(t, []string{"email"}, ru.eventUser.AlreadyFilteredAttributes)
}
