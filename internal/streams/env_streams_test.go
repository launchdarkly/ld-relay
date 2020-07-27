package streams

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

func TestConstructorRegistersChannels(t *testing.T) {
	store := makeMockStore(nil, nil)

	t.Run("with all credentials", func(t *testing.T) {
		testPubs := &sharedtest.TestPublishers{}
		es := NewEnvStreams(makePublishers(testPubs), store,
			testSDKKey, testMobileKey, testEnvID,
			0, ldlog.NewDisabledLoggers(),
		)
		require.NotNil(t, es)
		defer es.Close()

		assert.Len(t, testPubs.ServerSideAll.Repos, 1)
		assert.NotNil(t, testPubs.ServerSideAll.Repos[string(testSDKKey)])

		assert.Len(t, testPubs.ServerSideFlags.Repos, 1)
		assert.NotNil(t, testPubs.ServerSideFlags.Repos[string(testSDKKey)])

		assert.Len(t, testPubs.Mobile.Repos, 1)
		assert.NotNil(t, testPubs.Mobile.Repos[string(testMobileKey)])

		assert.Len(t, testPubs.JSClient.Repos, 1)
		assert.NotNil(t, testPubs.JSClient.Repos[string(testEnvID)])
	})

	t.Run("with SDK key only", func(t *testing.T) {
		testPubs := &sharedtest.TestPublishers{}
		es := NewEnvStreams(makePublishers(testPubs), store,
			testSDKKey, "", "",
			0, ldlog.NewDisabledLoggers(),
		)
		require.NotNil(t, es)
		defer es.Close()

		assert.Len(t, testPubs.ServerSideAll.Repos, 1)
		assert.NotNil(t, testPubs.ServerSideAll.Repos[string(testSDKKey)])

		assert.Len(t, testPubs.ServerSideFlags.Repos, 1)
		assert.NotNil(t, testPubs.ServerSideFlags.Repos[string(testSDKKey)])

		assert.Len(t, testPubs.Mobile.Repos, 0)
		assert.Len(t, testPubs.JSClient.Repos, 0)
	})
}

func TestSendAllDataUpdate(t *testing.T) {
	store := makeMockStore(nil, nil)
	testPubs := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(testPubs), store,
		testSDKKey, testMobileKey, testEnvID,
		time.Hour,
		ldlog.NewDisabledLoggers(),
	)
	require.NotNil(t, es)
	defer es.Close()

	es.SendAllDataUpdate(allData)

	assert.Equal(t, []sharedtest.PublishedEvent{{
		Channel: string(testSDKKey),
		Event:   MakeServerSidePutEvent(allData),
	}}, testPubs.ServerSideAll.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{{
		Channel: string(testSDKKey),
		Event:   MakeServerSideFlagsOnlyPutEvent(allData),
	}}, testPubs.ServerSideFlags.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{
		{Channel: string(testMobileKey), Event: MakePingEvent()},
	}, testPubs.Mobile.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{
		{Channel: string(testEnvID), Event: MakePingEvent()},
	}, testPubs.JSClient.Events)
}

func TestSendSingleItemUpdate(t *testing.T) {
	store := makeMockStore(nil, nil)
	testPubs := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(testPubs), store,
		testSDKKey, testMobileKey, testEnvID,
		time.Hour,
		ldlog.NewDisabledLoggers(),
	)
	require.NotNil(t, es)
	defer es.Close()

	es.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
	es.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1))

	assert.Equal(t, []sharedtest.PublishedEvent{
		{
			Channel: string(testSDKKey),
			Event:   MakeServerSidePatchEvent(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
		},
		{
			Channel: string(testSDKKey),
			Event:   MakeServerSidePatchEvent(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.SegmentDesc(testSegment1)),
		},
	}, testPubs.ServerSideAll.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{{
		Channel: string(testSDKKey),
		Event:   MakeServerSideFlagsOnlyPatchEvent(testFlag1.Key, sharedtest.FlagDesc(testFlag1)),
	}}, testPubs.ServerSideFlags.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{
		{Channel: string(testMobileKey), Event: MakePingEvent()},
		{Channel: string(testMobileKey), Event: MakePingEvent()},
	}, testPubs.Mobile.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{
		{Channel: string(testEnvID), Event: MakePingEvent()},
		{Channel: string(testEnvID), Event: MakePingEvent()},
	}, testPubs.JSClient.Events)
}

func TestSendSingleItemDelete(t *testing.T) {
	store := makeMockStore(nil, nil)
	testPubs := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(testPubs), store,
		testSDKKey, testMobileKey, testEnvID,
		time.Hour,
		ldlog.NewDisabledLoggers(),
	)
	require.NotNil(t, es)
	defer es.Close()

	es.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.DeletedItem(1))
	es.SendSingleItemUpdate(ldstoreimpl.Segments(), testSegment1.Key, sharedtest.DeletedItem(2))

	assert.Equal(t, []sharedtest.PublishedEvent{
		{
			Channel: string(testSDKKey),
			Event:   MakeServerSideDeleteEvent(ldstoreimpl.Features(), testFlag1.Key, 1),
		},
		{
			Channel: string(testSDKKey),
			Event:   MakeServerSideDeleteEvent(ldstoreimpl.Segments(), testSegment1.Key, 2),
		},
	}, testPubs.ServerSideAll.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{{
		Channel: string(testSDKKey),
		Event:   MakeServerSideFlagsOnlyDeleteEvent(testFlag1.Key, 1),
	}}, testPubs.ServerSideFlags.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{
		{Channel: string(testMobileKey), Event: MakePingEvent()},
		{Channel: string(testMobileKey), Event: MakePingEvent()},
	}, testPubs.Mobile.Events)

	assert.Equal(t, []sharedtest.PublishedEvent{
		{Channel: string(testEnvID), Event: MakePingEvent()},
		{Channel: string(testEnvID), Event: MakePingEvent()},
	}, testPubs.JSClient.Events)
}

func TestHeartbeats(t *testing.T) {
	heartbeatInterval := time.Millisecond * 20

	store := makeMockStore(nil, nil)
	testPubs := &sharedtest.TestPublishers{}
	es := NewEnvStreams(makePublishers(testPubs), store,
		testSDKKey, testMobileKey, testEnvID,
		heartbeatInterval,
		ldlog.NewDisabledLoggers(),
	)
	require.NotNil(t, es)
	defer es.Close()

	<-time.After(heartbeatInterval * 4)

	assert.GreaterOrEqual(t, len(testPubs.ServerSideAll.GetComments()), 2)
	assert.GreaterOrEqual(t, len(testPubs.ServerSideFlags.GetComments()), 2)
	assert.GreaterOrEqual(t, len(testPubs.Mobile.GetComments()), 2)
	assert.GreaterOrEqual(t, len(testPubs.JSClient.GetComments()), 2)
}
