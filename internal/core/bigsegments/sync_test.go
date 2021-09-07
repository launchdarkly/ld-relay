package bigsegments

import (
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bigSegmentStoreMock struct {
	cursor     string
	lock       sync.Mutex
	patchCh    chan bigSegmentPatch
	syncTimeCh chan ldtime.UnixMillisecondTime
}

func (s *bigSegmentStoreMock) applyPatch(patch bigSegmentPatch) (bool, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.cursor != patch.PreviousVersion {
		return false, nil
	}
	s.cursor = patch.Version

	s.patchCh <- patch

	return true, nil
}

func (s *bigSegmentStoreMock) getCursor() (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	return s.cursor, nil
}

func (s *bigSegmentStoreMock) setSynchronizedOn(synchronizedOn ldtime.UnixMillisecondTime) error {
	s.syncTimeCh <- synchronizedOn

	return nil
}

func (s *bigSegmentStoreMock) GetSynchronizedOn() (ldtime.UnixMillisecondTime, error) {
	return 0, nil
}

func (s *bigSegmentStoreMock) Close() error {
	return nil
}

func newBigSegmentStoreMock() *bigSegmentStoreMock {
	return &bigSegmentStoreMock{
		patchCh:    make(chan bigSegmentPatch, 100),
		syncTimeCh: make(chan ldtime.UnixMillisecondTime, 100),
	}
}

func assertPollRequest(t *testing.T, req httphelpers.HTTPRequestInfo, afterVersion string) {
	assert.Equal(t, string(testSDKKey), req.Request.Header.Get("Authorization"))
	assert.Equal(t, unboundedPollPath, req.Request.URL.Path)
	if afterVersion == "" {
		assert.Equal(t, "", req.Request.URL.RawQuery)
	} else {
		assert.Equal(t, "after="+afterVersion, req.Request.URL.RawQuery)
	}
}

func assertStreamRequest(t *testing.T, req httphelpers.HTTPRequestInfo) {
	assert.Equal(t, string(testSDKKey), req.Request.Header.Get("Authorization"))
	assert.Equal(t, unboundedStreamPath, req.Request.URL.Path)
}

func requirePatch(t *testing.T, s *bigSegmentStoreMock, expectedPatch bigSegmentPatch) {
	select {
	case patch := <-s.patchCh:
		require.Equal(t, expectedPatch, patch)
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for patch")
	}
}

func requireUpdates(t *testing.T, ch <-chan UpdatesSummary, expectedKeys []string) {
	select {
	case u := <-ch:
		sort.Strings(u.SegmentKeysUpdated)
		sort.Strings(expectedKeys)
		require.Equal(t, expectedKeys, u.SegmentKeysUpdated)
	case <-time.After(time.Second):
		require.Fail(t, "timed out waiting for updates")
	}
}

func TestBasicSync(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	patch1 := newPatchBuilder("segment.g1", "1", "").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	patch2 := newPatchBuilder("segment.g1", "2", "1").
		removeIncludes("included1").removeExcludes("excluded1").build()

	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{patch1}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
		),
	)

	sseHandler, _ := httphelpers.SSEHandler(makePatchEvent(patch2))
	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(sseHandler)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			startTime := ldtime.UnixMillisNow()

			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			defer segmentSync.Close()
			segmentSync.Start()

			updatesCh := segmentSync.SegmentUpdatesCh()
			go func() {
				for range updatesCh {
				}
			}() // just ensures that the synchronizer won't be blocked by the channel

			pollReq1 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)
			require.Equal(t, 0, len(storeMock.syncTimeCh))

			pollReq2 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			require.Equal(t, 0, len(storeMock.patchCh))

			sharedtest.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, patch2)

			sharedtest.ExpectNoTestRequests(t, streamRequestsCh, time.Millisecond*50)

			assert.Equal(t, []string{
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
			}, mockLog.GetOutput(ldlog.Info))
			assert.Len(t, mockLog.GetOutput(ldlog.Warn), 0)
		})
	})
}

func TestSyncSendsUpdates(t *testing.T) {
	// Scenario:
	// - Polling returns 3 patches (in 2 poll responses); these are aggregated into one UpdatesSummary
	// - Then the stream returns 1 more patch which generates another UpdatesSummary
	// We're also testing that segment IDs are aggregated into segment keys, i.e. "segment1.g1" and
	// "segment1.g2" together are reported as one update to "segment1".
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	poll1Patch1 := newPatchBuilder("segment1.g1", "1", "").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	poll2Patch1 := newPatchBuilder("segment1.g2", "2", "1").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	poll2Patch2 := newPatchBuilder("segment2.g3", "3", "2").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	streamPatch := newPatchBuilder("segment2.g3", "4", "3").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()

	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{poll1Patch1}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{poll2Patch1, poll2Patch2}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
		),
	)

	sseHandler, _ := httphelpers.SSEHandler(makePatchEvent(streamPatch))
	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(sseHandler)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			defer segmentSync.Close()
			segmentSync.Start()

			updatesCh := segmentSync.SegmentUpdatesCh()

			pollReq1 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, poll1Patch1)
			require.Equal(t, 0, len(storeMock.syncTimeCh))

			pollReq2 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, poll1Patch1.Version)
			requirePatch(t, storeMock, poll2Patch1)
			requirePatch(t, storeMock, poll2Patch2)

			pollReq3 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, poll2Patch2.Version)

			pollReq4 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq4, poll2Patch2.Version)

			require.Equal(t, 0, len(storeMock.patchCh))

			requireUpdates(t, updatesCh, []string{"segment1", "segment2"})

			sharedtest.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)

			streamReq1 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, streamPatch)

			sharedtest.ExpectNoTestRequests(t, streamRequestsCh, time.Millisecond*50)

			requireUpdates(t, updatesCh, []string{"segment2"})
		})
	})
}

func TestSyncSkipsOutOfOrderUpdateFromPoll(t *testing.T) {
	// Scenario:
	// - Poll returns 3 patches: first patch is valid, second patch is non-matching, third is matching
	// - We apply the first patch
	// - Second patch causes a warning and causes remainder of list to be skipped
	// - Then we proceed with stream request as usual
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	patch1 := newPatchBuilder("segment.g1", "1", "").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	patch1x := newPatchBuilder("segment.g1", "1x", "non-matching-previous-version").
		addIncludes("includedx").addExcludes("excludedx").build()
	patch1y := newPatchBuilder("segment.g1", "2", "1").
		addIncludes("includedy").addExcludes("excludedy").build()
	patch2 := newPatchBuilder("segment.g1", "2", "1").
		removeIncludes("included1").removeExcludes("excluded1").build()

	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{patch1, patch1x, patch1y}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
		),
	)

	sseHandler, _ := httphelpers.SSEHandler(makePatchEvent(patch2))
	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(sseHandler)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			startTime := ldtime.UnixMillisNow()

			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			defer segmentSync.Close()
			segmentSync.Start()

			pollReq1 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)
			require.Equal(t, 0, len(storeMock.syncTimeCh))

			pollReq2 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			require.Equal(t, 0, len(storeMock.patchCh))
			sharedtest.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, patch2)

			sharedtest.ExpectNoTestRequests(t, streamRequestsCh, time.Millisecond*50)

			assert.Equal(t, []string{
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
			}, mockLog.GetOutput(ldlog.Info))
			mockLog.AssertMessageMatch(t, true, ldlog.Warn, `"non-matching-previous-version" which was not the latest`)
		})
	})
}

func TestSyncSkipsOutOfOrderUpdateFromStreamAndRestartsStream(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	patch1 := newPatchBuilder("segment.g1", "1", "").
		addIncludes("included1", "included2").addExcludes("excluded1", "excluded2").build()
	patch2x := newPatchBuilder("segment.g1", "2", "non-matching-previous-version").
		removeIncludes("included1").removeExcludes("excluded1").build()
	patch2 := newPatchBuilder("segment.g1", "2", "1").
		removeIncludes("included1").removeExcludes("excluded1").build()

	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{patch1}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
		),
	)

	firstStream, _ := httphelpers.SSEHandler(makePatchEvent(patch2x))
	secondStream, _ := httphelpers.SSEHandler(makePatchEvent(patch2))
	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(firstStream, secondStream),
	)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			startTime := ldtime.UnixMillisNow()

			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			segmentSync.streamRetryInterval = time.Millisecond
			defer segmentSync.Close()
			segmentSync.Start()

			pollReq1 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)
			require.Equal(t, 0, len(storeMock.syncTimeCh))

			pollReq2 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			require.Equal(t, 0, len(storeMock.patchCh))

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)

			pollReq4 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq4, patch1.Version)

			streamReq2 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq2)

			pollReq5 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq5, patch1.Version)
			sharedtest.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)

			requirePatch(t, storeMock, patch2)

			sharedtest.ExpectNoTestRequests(t, streamRequestsCh, time.Millisecond*50)

			assert.Equal(t, []string{
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
			}, mockLog.GetOutput(ldlog.Info))
			mockLog.AssertMessageMatch(t, true, ldlog.Warn, `"non-matching-previous-version" which was not the latest`)
		})
	})
}

func TestSyncRetryIfStreamFails(t *testing.T) {
	// In this test, we set up a successful poll and stream. Then we force the stream to close.
	// The synchronizer should start over with a new poll and stream.
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	patch1 := newPatchBuilder("segment.g1", "1", "").build()
	patch2 := newPatchBuilder("segment.g1", "2", "1").build()
	patch3 := newPatchBuilder("segment.g1", "3", "2").build()
	patch4 := newPatchBuilder("segment.g1", "4", "3").build()

	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{patch1}, nil), // poll 1: initial connection
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),       // poll 2: completion of poll 1
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),       // poll 3: done in conjunction with stream 1
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{patch3}, nil), // poll 4: retry after stream fails
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),       // poll 5: completion of poll 4
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),       // poll 6: done in conjunction with stream 2
		),
	)

	sseHandler1, sseControl1 := httphelpers.SSEHandler(makePatchEvent(patch2))
	sseHandler2, _ := httphelpers.SSEHandler(makePatchEvent(patch4))
	streamsHandler, streamRequestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(sseHandler1, sseHandler2),
	)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamsHandler, func(streamServer *httptest.Server) {
			startTime := ldtime.UnixMillisNow()

			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			segmentSync.streamRetryInterval = time.Millisecond
			defer segmentSync.Close()
			segmentSync.Start()

			pollReq1 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)
			require.Equal(t, 0, len(storeMock.syncTimeCh))

			pollReq2 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			require.Equal(t, 0, len(storeMock.patchCh))

			sharedtest.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, patch2)

			sharedtest.ExpectNoTestRequests(t, streamRequestsCh, time.Millisecond*50)

			// Now cause stream 1 to close
			sseControl1.Close()

			// Expect another poll+stream cycle; this time we get patch3 from the poll, patch4 from the stream
			pollReq4 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq4, patch2.Version)
			requirePatch(t, storeMock, patch3)

			pollReq5 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq5, patch3.Version)

			pollReq6 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq6, patch3.Version)

			streamReq2 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq2)
			requirePatch(t, storeMock, patch4)

			sharedtest.ExpectNoTestRequests(t, streamRequestsCh, time.Millisecond*50)

			assert.Equal(t, []string{
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
			}, mockLog.GetOutput(ldlog.Info))
			assert.Equal(t, []string{
				"BigSegmentSynchronizer: Stream connection failed: EOF",
				"BigSegmentSynchronizer: Will retry",
				"BigSegmentSynchronizer: Re-established connection",
			}, mockLog.GetOutput(ldlog.Warn))
			assert.Len(t, mockLog.GetOutput(ldlog.Error), 0)
		})
	})
}
