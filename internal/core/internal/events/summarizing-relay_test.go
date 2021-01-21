package events

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/store"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

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
	r := st.ExpectTestRequest(t, requestsCh, time.Second)
	assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
	assert.Equal(t, string(st.EnvMain.Config.SDKKey), r.Request.Header.Get("Authorization"))
	return string(r.Body)
}

func TestSummarizeFeatureEventsForExistingFlag(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlag(false, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutput, payload)
	})
}

func TestSummarizeFeatureEventsForExistingFlagWithTrackEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlag(true, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputTrackEvents, payload)
	})
}

func TestSummarizeFeatureEventsForExistingFlagWithInlineUsersAndInlineUsers(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{InlineUsers: true}, func(p eventRelayTestParams) {
		flag := makeTestFlag(true, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputTrackEventsInlineUsers, payload)
	})
}

func TestSummarizeFeatureEventsForExistingFlagWithDebugEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlag(false, ldtime.UnixMillisNow()+1000000)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputDebugEvents, payload)
	})
}

func TestSummarizeFeatureEventsForUnknownFlagWithoutVersion(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsWithoutVersion), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputUnknownFlagWithoutVersion, payload)
	})
}

func TestSummarizeFeatureEventsForUnknownFlagWithEventVersion(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputUnknownFlagWithVersion, payload)
	})
}

func TestSummarizeSchemaV2FeatureEventsForExistingFlag(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		flag := makeTestFlagForPHP(false, 0)
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2), headersWithEventSchema(2))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutput, payload)
	})
}

func TestSummarizeSchemaV2FeatureEventsForExistingFlagWithTrackEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		// Here we're not setting trackEvents in the flag; it's specified by the event from the PHP SDK.
		flag := makeTestFlag(false, 0)
		flag.Variations = []ldvalue.Value{ldvalue.String("x"), ldvalue.String("y")}
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2TrackEvents), headersWithEventSchema(2))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputTrackEvents, string(payload))
	})
}

func TestSummarizeSchemaV2FeatureEventsForExistingFlagWithDebugEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		// Here we're not setting debugEventsUntilDate in the flag; it's specified by the event from the PHP SDK.
		flag := makeTestFlag(false, 0)
		flag.Variations = []ldvalue.Value{ldvalue.String("x"), ldvalue.String("y")}
		_, _ = st.UpsertFlag(p.dataStore, flag)

		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2DebugEvents), headersWithEventSchema(2))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputDebugEvents, string(payload))
	})
}

func TestSummarizeSchemaV2FeatureEventsForUnknownFlag(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableFeatureEventsSchemaV2WithoutVersion), headersWithEventSchema(2))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedFeatureEventsOutputUnknownFlagWithoutVersion, payload)
	})
}

func TestSummarizeCustomEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableCustomEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, expectedSummarizedCustomEvents, payload)
	})
}

func TestSummarizeCustomEventsWithInlineUsersLeavesEventsUnchanged(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{InlineUsers: true}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(summarizableCustomEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, summarizableCustomEvents, payload)
	})
}

func TestSummarizeIdentifyEventsLeavesEventsUnchanged(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(identifyEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, identifyEvents, payload)
	})
}

func TestSummarizeAliasEventsLeavesEventsUnchanged(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(aliasEvents), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, aliasEvents, payload)
	})
}

func TestSummarizingRelayDiscardsMalformedEvents(t *testing.T) {
	eventRelayTest(t, st.EnvMain, config.EventsConfig{}, func(p eventRelayTestParams) {
		req := st.BuildRequest("POST", "/", []byte(malformedEventsAndGoodIdentifyEventsInWellFormedJSON), headersWithEventSchema(0))
		p.dispatcher.GetHandler(sdks.Server, ldevents.AnalyticsEventDataKind)(httptest.NewRecorder(), req)
		p.dispatcher.flush()

		payload := expectSummarizedPayload(t, p.requestsCh)
		assert.JSONEq(t, identifyEvents, payload)
	})
}

func TestCanSeePrivateAttrsOfPHPEventUser(t *testing.T) {
	var ru receivedEventUser
	require.NoError(t, json.Unmarshal([]byte(`{"key": "k", "name": "n", "privateAttrs": ["email"]}`), &ru))
	assert.Equal(t, lduser.NewUserBuilder("k").Name("n").Build(), ru.eventUser.User)
	assert.Equal(t, []string{"email"}, ru.eventUser.AlreadyFilteredAttributes)
}
