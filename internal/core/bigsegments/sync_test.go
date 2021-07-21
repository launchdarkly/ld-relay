package bigsegments

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
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
		syncTimeCh: make(chan ldtime.UnixMillisecondTime),
	}
}

func TestBasicSync(t *testing.T) {
	pollHandler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{patch1}, nil),
			httphelpers.HandlerWithJSONResponse([]bigSegmentPatch{}, nil),
		),
	)

	patch2Encoded, err := json.Marshal([]bigSegmentPatch{patch2})
	require.NoError(t, err)

	sseHandler, _ := httphelpers.SSEHandler(&httphelpers.SSEEvent{
		Data: string(patch2Encoded),
	})

	streamHandler, streamRequestsCh := httphelpers.RecordingHandler(sseHandler)

	httphelpers.WithServer(pollHandler, func(pollServer *httptest.Server) {
		httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
			sdkKey := config.SDKKey("sdk-abc")
			startTime := ldtime.UnixMillisNow()

			storeMock := newBigSegmentStoreMock()
			defer storeMock.Close()

			httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDisabledLoggers())
			require.NoError(t, err)

			segmentSync := newDefaultBigSegmentSynchronizer(httpConfig, storeMock,
				pollServer.URL, streamServer.URL, config.EnvironmentID("env-xyz"), sdkKey, ldlog.NewDefaultLoggers())
			defer segmentSync.Close()
			segmentSync.Start()

			requestInfo1 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assert.Equal(t, string(sdkKey), requestInfo1.Request.Header.Get("Authorization"))
			assert.Equal(t, unboundedPollPath, requestInfo1.Request.URL.Path)
			assert.Equal(t, "", requestInfo1.Request.URL.RawQuery)
			patch := <-storeMock.patchCh
			require.Equal(t, patch1.Version, patch.Version)
			require.Equal(t, 0, len(storeMock.syncTimeCh))

			requestInfo2 := sharedtest.ExpectTestRequest(t, requestsCh, time.Second)
			assert.Equal(t, string(sdkKey), requestInfo2.Request.Header.Get("Authorization"))
			assert.Equal(t, unboundedPollPath, requestInfo2.Request.URL.Path)
			assert.Equal(t, "after="+patch1.Version, requestInfo2.Request.URL.RawQuery)
			require.Equal(t, 0, len(storeMock.patchCh))
			require.Equal(t, 0, len(requestsCh))

			syncTime := <-storeMock.syncTimeCh
			assert.True(t, syncTime >= startTime)
			assert.True(t, syncTime <= ldtime.UnixMillisNow())

			requestInfo3 := sharedtest.ExpectTestRequest(t, streamRequestsCh, time.Second)
			assert.Equal(t, string(sdkKey), requestInfo2.Request.Header.Get("Authorization"))
			assert.Equal(t, unboundedStreamPath, requestInfo3.Request.URL.Path)
			patch = <-storeMock.patchCh
			assert.Equal(t, patch2.Version, patch.Version)

			require.Equal(t, 0, len(streamRequestsCh))
		})
	})
}
