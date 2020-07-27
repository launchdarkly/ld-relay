package streams

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

func verifyReplayedEvent(
	t *testing.T,
	tp *sharedtest.TestPublisher,
	channel string,
	expectedEvent eventsource.Event,
) {
	repo := tp.Repos[channel]
	require.NotNil(t, repo)
	eventsCh := repo.Replay(channel, "")
	require.NotNil(t, eventsCh)
	events := readAllEvents(eventsCh)
	assert.Equal(t, []eventsource.Event{expectedEvent}, events)
}

func verifyNoReplayedEvent(
	t *testing.T,
	tp *sharedtest.TestPublisher,
	channel string,
) {
	repo := tp.Repos[channel]
	require.NotNil(t, repo)
	eventsCh := repo.Replay(channel, "")
	require.NotNil(t, eventsCh)
	events := readAllEvents(eventsCh)
	assert.Len(t, events, 0)
}

func TestInitialEventForEmptyStore(t *testing.T) {
	store := makeMockStore(nil, nil)
	tp := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(tp), store,
		testSDKKey, testMobileKey, testEnvID,
		0, ldlog.NewDisabledLoggers(),
	)
	defer es.Close()

	verifyReplayedEvent(t, &tp.ServerSideAll, string(testSDKKey), MakeServerSidePutEvent(nil))
	verifyReplayedEvent(t, &tp.ServerSideFlags, string(testSDKKey), MakeServerSideFlagsOnlyPutEvent(nil))
	verifyReplayedEvent(t, &tp.Mobile, string(testMobileKey), MakePingEvent())
	verifyReplayedEvent(t, &tp.JSClient, string(testEnvID), MakePingEvent())
}

func TestInitialEventForNonEmptyStore(t *testing.T) {
	store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
	allData := []ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: store.flags},
		{Kind: ldstoreimpl.Segments(), Items: store.segments},
	}
	tp := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(tp), store,
		testSDKKey, testMobileKey, testEnvID,
		0, ldlog.NewDisabledLoggers(),
	)
	defer es.Close()

	verifyReplayedEvent(t, &tp.ServerSideAll, string(testSDKKey), MakeServerSidePutEvent(allData))
	verifyReplayedEvent(t, &tp.ServerSideFlags, string(testSDKKey), MakeServerSideFlagsOnlyPutEvent(allData))
	verifyReplayedEvent(t, &tp.Mobile, string(testMobileKey), MakePingEvent())
	verifyReplayedEvent(t, &tp.JSClient, string(testEnvID), MakePingEvent())
}

func TestInitialEventForUninitializedStore(t *testing.T) {
	store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
	store.initialized = false
	tp := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(tp), store,
		testSDKKey, testMobileKey, testEnvID,
		0, ldlog.NewDisabledLoggers(),
	)
	defer es.Close()

	verifyNoReplayedEvent(t, &tp.ServerSideAll, string(testSDKKey))
	verifyNoReplayedEvent(t, &tp.ServerSideFlags, string(testSDKKey))
	verifyReplayedEvent(t, &tp.Mobile, string(testMobileKey), MakePingEvent())
	verifyReplayedEvent(t, &tp.JSClient, string(testEnvID), MakePingEvent())
}

func TestInitialEventForStoreWithErrorOnFlagsQuery(t *testing.T) {
	store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
	store.fakeFlagsError = fakeError
	tp := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(tp), store,
		testSDKKey, testMobileKey, testEnvID,
		0, ldlog.NewDisabledLoggers(),
	)
	defer es.Close()

	verifyNoReplayedEvent(t, &tp.ServerSideAll, string(testSDKKey))
	verifyNoReplayedEvent(t, &tp.ServerSideFlags, string(testSDKKey))
	verifyReplayedEvent(t, &tp.Mobile, string(testMobileKey), MakePingEvent())
	verifyReplayedEvent(t, &tp.JSClient, string(testEnvID), MakePingEvent())
}

func TestInitialEventForStoreWithErrorOnSegmentsQuery(t *testing.T) {
	store := makeMockStore([]ldmodel.FeatureFlag{testFlag1, testFlag2}, []ldmodel.Segment{testSegment1})
	flagsData := []ldstoretypes.Collection{{Kind: ldstoreimpl.Features(), Items: store.flags}}
	store.fakeSegmentsError = fakeError
	tp := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(tp), store,
		testSDKKey, testMobileKey, testEnvID,
		0, ldlog.NewDisabledLoggers(),
	)
	defer es.Close()

	verifyNoReplayedEvent(t, &tp.ServerSideAll, string(testSDKKey))
	verifyReplayedEvent(t, &tp.ServerSideFlags, string(testSDKKey), MakeServerSideFlagsOnlyPutEvent(flagsData))
	verifyReplayedEvent(t, &tp.Mobile, string(testMobileKey), MakePingEvent())
	verifyReplayedEvent(t, &tp.JSClient, string(testEnvID), MakePingEvent())
}
