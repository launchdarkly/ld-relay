package bigsegments

import (
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/retry"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

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
	patch := helpers.RequireValue(t, s.patchCh, time.Second, "timed out waiting for patch")
	require.Equal(t, expectedPatch, patch)
}

func requireNoMorePatches(t *testing.T, s *bigSegmentStoreMock) {
	if len(s.patchCh) > 0 {
		var patches []bigSegmentPatch
		for len(s.patchCh) > 0 {
			patches = append(patches, <-s.patchCh)
		}
		require.Fail(t, "did not expect any more patches, but got some", "patches: %+v", patches)
	}
}

func requireUpdates(t *testing.T, ch <-chan UpdatesSummary, expectedKeys []string) {
	u := helpers.RequireValue(t, ch, time.Second, "timed out waiting for updates")
	sort.Strings(u.SegmentKeysUpdated)
	sort.Strings(expectedKeys)
	require.Equal(t, expectedKeys, u.SegmentKeysUpdated)
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

			pollReq1 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)

			pollReq2 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			if !helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, patch2)

			if !helpers.AssertNoMoreValues(t, streamRequestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			requireNoMorePatches(t, storeMock)

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

			pollReq1 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, poll1Patch1)

			pollReq2 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, poll1Patch1.Version)
			requirePatch(t, storeMock, poll2Patch1)
			requirePatch(t, storeMock, poll2Patch2)

			pollReq3 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, poll2Patch2.Version)

			pollReq4 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq4, poll2Patch2.Version)

			requireUpdates(t, updatesCh, []string{"segment1", "segment2"})

			if !helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			streamReq1 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, streamPatch)

			if !helpers.AssertNoMoreValues(t, streamRequestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			requireNoMorePatches(t, storeMock)

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

			pollReq1 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)

			pollReq2 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			if !helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, patch2)

			if !helpers.AssertNoMoreValues(t, streamRequestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			requireNoMorePatches(t, storeMock)

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
			segmentSync.retryStrategy = fastRetryStrategy()
			defer segmentSync.Close()
			segmentSync.Start()

			pollReq1 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)

			pollReq2 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)

			pollReq4 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq4, patch1.Version)

			streamReq2 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq2)

			pollReq5 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq5, patch1.Version)
			if !helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			requirePatch(t, storeMock, patch2)

			if !helpers.AssertNoMoreValues(t, streamRequestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			requireNoMorePatches(t, storeMock)

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
			segmentSync.retryStrategy = fastRetryStrategy()
			defer segmentSync.Close()
			segmentSync.Start()

			pollReq1 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq1, "")
			requirePatch(t, storeMock, patch1)

			pollReq2 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq2, patch1.Version)

			pollReq3 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq3, patch1.Version)

			if !helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			streamReq1 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq1)
			requirePatch(t, storeMock, patch2)

			if !helpers.AssertNoMoreValues(t, streamRequestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			// Now cause stream 1 to close
			sseControl1.Close()

			// Expect another poll+stream cycle; this time we get patch3 from the poll, patch4 from the stream
			pollReq4 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq4, patch2.Version)
			requirePatch(t, storeMock, patch3)

			pollReq5 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq5, patch3.Version)

			pollReq6 := helpers.RequireValue(t, requestsCh, time.Second)
			assertPollRequest(t, pollReq6, patch3.Version)

			streamReq2 := helpers.RequireValue(t, streamRequestsCh, time.Second)
			assertStreamRequest(t, streamReq2)
			requirePatch(t, storeMock, patch4)

			if !helpers.AssertNoMoreValues(t, streamRequestsCh, time.Millisecond*50) {
				t.FailNow()
			}

			requireNoMorePatches(t, storeMock)

			assert.Equal(t, []string{
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
				"BigSegmentSynchronizer: Applied 1 update",
			}, mockLog.GetOutput(ldlog.Info))
			warnOutput := mockLog.GetOutput(ldlog.Warn)
			require.Len(t, warnOutput, 3)
			assert.Equal(t, "BigSegmentSynchronizer: Stream connection failed: EOF", warnOutput[0])
			// The retry delay carries jitter, so match the message and not the duration.
			assert.Regexp(t, `^BigSegmentSynchronizer: Will retry in \S+$`, warnOutput[1])
			assert.Equal(t, "BigSegmentSynchronizer: Re-established connection", warnOutput[2])
			assert.Len(t, mockLog.GetOutput(ldlog.Error), 0)
		})
	})
}

// fastRetryStrategy scales this component's retry binding down so a test does not wait out
// real delays. The proportions are kept so the curve still doubles and clamps. Options let a
// test remove jitter or supply a clock when it needs to assert an exact wait.
func fastRetryStrategy(options ...retry.Option) *retry.Strategy {
	return retry.NewStrategy(fastRetryConfig(), options...)
}

// fastRetryConfig is the scaled-down binding behind fastRetryStrategy.
func fastRetryConfig() retry.Config {
	return retry.Config{
		InitialDelay:         time.Millisecond,
		NormalCeiling:        3 * time.Millisecond,
		ExtendedInitialDelay: 30 * time.Millisecond,
		ExtendedCeiling:      360 * time.Millisecond,
		ResetThreshold:       6 * time.Millisecond,
	}
}

// zeroJitter removes jitter so a test can assert an exact retry delay.
func zeroJitter() retry.Option {
	return retry.WithJitter(func(time.Duration) time.Duration { return 0 })
}

// testClock is advanced explicitly by the test, never as a side effect of being read, so a
// test asserts on time it controls rather than on how many times the code reads the clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// retryDelays returns the delay from each "Will retry in ..." warning, in order. Parsing the
// duration back out keeps the assertions about time rather than about log formatting.
func retryDelays(t *testing.T, mockLog *ldlogtest.MockLog) []time.Duration {
	t.Helper()
	var out []time.Duration
	for _, line := range mockLog.GetOutput(ldlog.Warn) {
		_, after, found := strings.Cut(line, "Will retry in ")
		if !found {
			continue
		}
		d, err := time.ParseDuration(strings.TrimSpace(after))
		require.NoError(t, err, "could not parse a delay out of %q", line)
		out = append(out, d)
	}
	return out
}

// drainUpdates consumes the updates channel so notifySegmentsUpdated never blocks the
// supervisor while a test is only interested in retry timing.
func drainUpdates(ch <-chan UpdatesSummary) {
	go func() {
		for range ch {
		}
	}()
}

// hasLogMessage reports whether any line at the given level contains substr.
func hasLogMessage(mockLog *ldlogtest.MockLog, level ldlog.LogLevel, substr string) bool {
	for _, line := range mockLog.GetOutput(level) {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func TestSyncKeepsRetryingAfterUnauthorized(t *testing.T) {
	// A rejected SDK key must not stop the synchronizer. An operator can make the key valid
	// again without Relay knowing, so it keeps trying, but on the extended delays so that a
	// fleet of Relay Proxy instances does not hammer a service rejecting every request.
	//
	// Before the retry work, the synchronizer intended to stop here and failed to: it
	// returned a *httpStatusError while testing for a value-typed httpStatusError, so the
	// branch never ran. This test pins the behavior either way.
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	pollHandler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(401))

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(httphelpers.HandlerWithStatus(401), func(streamServer *httptest.Server) {
			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			segmentSync.retryStrategy = fastRetryStrategy()
			defer segmentSync.Close()
			segmentSync.Start()

			// Three attempts show it did not give up after the first rejection.
			for i := 1; i <= 3; i++ {
				helpers.RequireValue(t, requestsCh, time.Second, "expected poll attempt %d", i)
			}

			// Keep reading the recorder while waiting for the log line. If a regression left
			// the synchronizer on the normal delays it would poll every few milliseconds; the
			// recorder channel then fills, its handler blocks, and the server's Close hangs the
			// package for the full test timeout instead of failing here.
			deadline := time.After(time.Second)
			for !hasLogMessage(mockLog, ldlog.Info, "engaging extended backoff") {
				select {
				case <-requestsCh:
				case <-deadline:
					require.FailNow(t, "expected the extended delays to be engaged")
				}
			}

			// An unexpected failure is worth an error even though it recovers on its own.
			assert.True(t, hasLogMessage(mockLog, ldlog.Error, "Synchronization failed"))
		})
	})
}

func TestSyncStaysOnNormalDelaysAfterServerError(t *testing.T) {
	// A 5xx is transient, so it must not engage the extended delays. This is the other half
	// of the classification: without it, a test that only covers 401 would pass even if
	// every failure were treated as unexpected.
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	pollHandler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(503))

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(httphelpers.HandlerWithStatus(503), func(streamServer *httptest.Server) {
			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			segmentSync.retryStrategy = fastRetryStrategy()
			defer segmentSync.Close()
			segmentSync.Start()

			for i := 1; i <= 3; i++ {
				helpers.RequireValue(t, requestsCh, time.Second, "expected poll attempt %d", i)
			}

			assert.False(t, hasLogMessage(mockLog, ldlog.Info, "engaging extended backoff"),
				"a 503 is transient and must stay on the normal delays")
			assert.False(t, hasLogMessage(mockLog, ldlog.Error, "Synchronization failed"),
				"a transient failure logs at warn, not error")
			assert.True(t, hasLogMessage(mockLog, ldlog.Warn, "Synchronization failed"))
		})
	})
}

func TestSyncReturnsToNormalDelaysAfterHealthyPeriod(t *testing.T) {
	// Once a rejected key is valid again, ResetThreshold of continuous healthy operation must
	// return the synchronizer to the normal delays. Healthy operation here is setSynced
	// succeeding, so this pins the OnHealthy call in setSynced; without it the extended
	// ceiling would persist for the life of the process.
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	patch1 := newPatchBuilder("segment.g1", "1", "").build()

	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithStatus(401),                            // rejected key: extended delays engage
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil), // key valid again
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil), // completes alongside the stream
		),
	)
	sseHandler, sseControl := httphelpers.SSEHandler(nil)
	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(sseHandler)

	clock := &testClock{t: time.Now()}

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			segmentSync.retryStrategy = fastRetryStrategy(zeroJitter(), retry.WithClock(clock.now))
			defer segmentSync.Close()
			segmentSync.Start()
			drainUpdates(segmentSync.SegmentUpdatesCh())

			helpers.RequireValue(t, requestsCh, time.Second, "expected the rejected poll")
			helpers.RequireValue(t, requestsCh, time.Second, "expected the poll after recovery")
			helpers.RequireValue(t, requestsCh, time.Second, "expected the poll alongside the stream")
			helpers.RequireValue(t, streamRequestsCh, time.Second, "expected the stream request")

			// The first health mark starts the healthy period.
			helpers.RequireValue(t, storeMock.syncTimeCh, time.Second, "expected the first health mark")

			// Advance past the reset threshold, then cause a second mark. Advancing here rather
			// than on every clock read keeps the assertion independent of how often the
			// implementation reads the clock.
			clock.advance(fastRetryConfig().ResetThreshold)
			sseControl.Enqueue(*makePatchEvent(patch1))
			requirePatch(t, storeMock, patch1)
			helpers.RequireValue(t, storeMock.syncTimeCh, time.Second, "expected the second health mark")

			// Drop the healthy stream. That is an ordinary failure, so the wait must come from
			// the normal curve, not the extended one it was on before the reset.
			sseControl.EndAll()

			require.Eventually(t, func() bool { return len(retryDelays(t, mockLog)) >= 2 },
				time.Second, time.Millisecond, "expected a second retry delay to be logged")
			delays := retryDelays(t, mockLog)
			cfg := fastRetryConfig()
			assert.Equal(t, cfg.ExtendedInitialDelay, delays[0], "the rejected key waits the extended base delay")
			assert.Equal(t, cfg.InitialDelay, delays[1],
				"after the reset threshold of healthy operation the wait returns to the normal base delay")
		})
	})
}

func TestSyncBacksOffExponentiallyAcrossStreamEnds(t *testing.T) {
	// A stream that ends reaches the supervisor as a nil error. It must still count as an
	// ordinary failure, so consecutive stream ends double the wait and clamp at the normal
	// ceiling rather than retrying on a flat interval.
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	pollHandler := httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil)
	sseHandler, sseControl := httphelpers.SSEHandler(nil)
	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(sseHandler)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			segmentSync := newDefaultBigSegmentSynchronizer(sharedtest.MakeBasicHTTPConfig(), storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), testSDKKey, mockLog.Loggers, "")
			// Each cycle marks the store once and then fails, and a failure ends the healthy
			// period, so no reset can interfere with the doubling. The real clock is therefore
			// safe here.
			segmentSync.retryStrategy = fastRetryStrategy(zeroJitter())
			defer segmentSync.Close()
			segmentSync.Start()
			drainUpdates(segmentSync.SegmentUpdatesCh())

			for i := 1; i <= 3; i++ {
				helpers.RequireValue(t, streamRequestsCh, time.Second, "expected stream connection %d", i)
				helpers.RequireValue(t, storeMock.syncTimeCh, time.Second, "expected the store to be marked synchronized")
				sseControl.EndAll()
			}

			require.Eventually(t, func() bool { return len(retryDelays(t, mockLog)) >= 3 },
				time.Second, time.Millisecond, "expected three retry delays to be logged")
			cfg := fastRetryConfig()
			assert.Equal(t, []time.Duration{
				cfg.InitialDelay,     // 1ms
				2 * cfg.InitialDelay, // 2ms
				cfg.NormalCeiling,    // 4ms clamped to 3ms
			}, retryDelays(t, mockLog)[:3], "consecutive stream ends must double and clamp")
		})
	})
}
