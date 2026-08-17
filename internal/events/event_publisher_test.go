package events

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v9/internal/util"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	m "github.com/launchdarkly/go-test-helpers/v3/matchers"

	"github.com/stretchr/testify/assert"
)

const testSDKKey = config.SDKKey("my-key")

func defaultHTTPConfig() httpconfig.HTTPConfig {
	hc, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", slog.Default())
	if err != nil {
		panic(err)
	}
	return hc
}

func TestHTTPEventPublisherSimple(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(), OptionBaseURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello again"`))
		publisher.Flush()
		r := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, "/bulk", r.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))

		uncompressed, err := util.DecompressGzipData([]byte(r.Body))
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["hello", "hello again"]`))
	})
}

func TestHTTPEventPublisherMultiQueuesWithMetadata(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(), OptionBaseURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{Tags: "a"}, json.RawMessage(`"hello"`))
		publisher.Publish(EventPayloadMetadata{Tags: "b"}, json.RawMessage(`"ok"`))
		publisher.Publish(EventPayloadMetadata{Tags: "a"}, json.RawMessage(`"hello again"`))
		publisher.Publish(EventPayloadMetadata{Tags: "b"}, json.RawMessage(`"thanks"`))
		publisher.Publish(EventPayloadMetadata{Tags: "a", SchemaVersion: 3}, json.RawMessage(`"also this"`))
		publisher.Flush()

		var received []httphelpers.HTTPRequestInfo
		for i := 0; i < 3; i++ {
			received = append(received, helpers.RequireValue(t, requestsCh, time.Second))
		}
		requestSortKey := func(r httphelpers.HTTPRequestInfo) string {
			return r.Request.Header.Get(EventSchemaHeader) + "," + r.Request.Header.Get(TagsHeader)
		}
		sort.Slice(received, func(i, j int) bool { return requestSortKey(received[i]) < requestSortKey(received[j]) })
		r0, r1, r2 := received[0], received[1], received[2]

		assert.Equal(t, "/bulk", r0.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r0.Request.Header.Get("Authorization"))
		assert.Equal(t, "3", r0.Request.Header.Get(EventSchemaHeader))
		assert.Equal(t, "a", r0.Request.Header.Get(TagsHeader))

		uncompressed0, err := util.DecompressGzipData(r0.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed0, m.JSONStrEqual(`["also this"]`))

		assert.Equal(t, "/bulk", received[0].Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r1.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r1.Request.Header.Get(EventSchemaHeader))
		assert.Equal(t, "a", r1.Request.Header.Get(TagsHeader))

		uncompressed1, err := util.DecompressGzipData(r1.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed1, m.JSONStrEqual(`["hello", "hello again"]`))

		assert.Equal(t, "/bulk", r2.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r2.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r2.Request.Header.Get(EventSchemaHeader))
		assert.Equal(t, "b", r2.Request.Header.Get(TagsHeader))

		uncompressed2, err := util.DecompressGzipData(r2.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed2, m.JSONStrEqual(`["ok", "thanks"]`))
	})
}

func TestHTTPEventPublisherOptionURIPath(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionURIPath("/special-path"))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()
		r := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, "/special-path", r.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))

		uncompressed, err := util.DecompressGzipData(r.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["hello"]`))
	})
}

func TestHTTPEventPublisherClosesImmediatelyAndOnlyOnce(t *testing.T) {
	publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default())
	timeout := time.After(time.Second)
	publisher.Close()
	publisher.Close()
	assert.Len(t, timeout, 0, "expected timeout to not have triggered but it did")
}

func TestHTTPPublisherAutomaticFlush(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionFlushInterval(time.Millisecond))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		r := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, "/bulk", r.Request.URL.Path)

		uncompressed, err := util.DecompressGzipData(r.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["hello"]`))
		assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
	})
}

func TestHTTPEventPublisherFlushDoesNothingIfThereAreNoEvents(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionFlushInterval(time.Millisecond))
		defer publisher.Close()
		publisher.Flush()
		helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50)
	})
}

func TestHTTPEventPublisherCapacity(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionCapacity(1))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"goodbye"`))
		publisher.Flush()
		r := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, "/bulk", r.Request.URL.Path)
		assert.Equal(t, strconv.Itoa(CurrentEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))

		uncompressed, err := util.DecompressGzipData(r.Body)
		assert.NoError(t, err)

		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["hello"]`))
	})
}

type mockEventMetrics struct {
	mu                 sync.Mutex
	droppedCount       int
	sentCount          int
	failedSendCount    int
	lastFailedSendMeta EventSendFailureMetadata
	bytesSent          int
	lastPendingEvents  int
}

func (m *mockEventMetrics) RecordDroppedEvents(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.droppedCount += count
}

func (m *mockEventMetrics) RecordEventsSent(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentCount += count
}

func (m *mockEventMetrics) RecordEventsFailedSend(count int, metadata EventSendFailureMetadata) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failedSendCount += count
	m.lastFailedSendMeta = metadata
}

func (m *mockEventMetrics) RecordEventsBytesSent(bytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytesSent += bytes
}

func (m *mockEventMetrics) RecordPendingEvents(depth int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastPendingEvents = depth
}

func (m *mockEventMetrics) getDroppedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.droppedCount
}

func (m *mockEventMetrics) getSentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sentCount
}

func (m *mockEventMetrics) getFailedSendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failedSendCount
}

func (m *mockEventMetrics) getLastFailedSendMeta() EventSendFailureMetadata {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastFailedSendMeta
}

func (m *mockEventMetrics) getBytesSent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bytesSent
}

func (m *mockEventMetrics) getLastPendingEvents() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastPendingEvents
}

func TestHTTPEventPublisherDroppedEventsMetric(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		metrics := &mockEventMetrics{}
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionCapacity(2), OptionEventMetrics{EventMetrics: metrics})
		defer publisher.Close()

		// Publish 5 events with capacity 2 — should drop 3
		publisher.Publish(EventPayloadMetadata{},
			json.RawMessage(`"a"`), json.RawMessage(`"b"`),
			json.RawMessage(`"c"`), json.RawMessage(`"d"`), json.RawMessage(`"e"`))
		publisher.Flush()

		r := helpers.RequireValue(t, requestsCh, time.Second)
		uncompressed, err := util.DecompressGzipData(r.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["a","b"]`))

		assert.Equal(t, 3, metrics.getDroppedCount())
	})
}

func TestHTTPEventPublisherEventsSentMetric(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		metrics := &mockEventMetrics{}
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionEventMetrics{EventMetrics: metrics})
		defer publisher.Close()

		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"a"`), json.RawMessage(`"b"`), json.RawMessage(`"c"`))
		publisher.Flush()

		_ = helpers.RequireValue(t, requestsCh, time.Second)
		// Wait for the goroutine to record the metric after the send completes
		assert.Eventually(t, func() bool { return metrics.getSentCount() == 3 }, time.Second, 10*time.Millisecond)
		assert.Greater(t, metrics.getBytesSent(), 0)
	})
}

func TestHTTPEventPublisherPendingEventsMetric(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		metrics := &mockEventMetrics{}
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionEventMetrics{EventMetrics: metrics})
		defer publisher.Close()

		// Publish 3 events — queue depth should be 3 after processing
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"a"`), json.RawMessage(`"b"`), json.RawMessage(`"c"`))
		// Flush will clear the queue — depth should go to 0
		publisher.Flush()

		_ = helpers.RequireValue(t, requestsCh, time.Second)
		assert.Eventually(t, func() bool { return metrics.getLastPendingEvents() == 0 }, time.Second, 10*time.Millisecond)
	})
}

func TestHTTPEventPublisherEventsFailedSendMetric(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	// Return 500 twice (exhausts retries) so the send fails
	handler, requestsCh := httphelpers.RecordingHandler(
		httphelpers.SequentialHandler(
			httphelpers.HandlerWithStatus(503),
			httphelpers.HandlerWithStatus(503),
		),
	)
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		metrics := &mockEventMetrics{}
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionEventMetrics{EventMetrics: metrics})
		defer publisher.Close()

		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"a"`), json.RawMessage(`"b"`))
		publisher.Flush()

		// Wait for both retry attempts
		_ = helpers.RequireValue(t, requestsCh, 5*time.Second)
		_ = helpers.RequireValue(t, requestsCh, 5*time.Second)

		assert.Eventually(t, func() bool { return metrics.getFailedSendCount() == 2 }, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, 0, metrics.getSentCount())
		assert.Equal(t, 503, metrics.getLastFailedSendMeta().StatusCode)
	})
}

func TestHTTPEventPublisherDroppedEventsMetricNilIsNoOp(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		// No EventMetrics option — should not panic
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionCapacity(1))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`), json.RawMessage(`"goodbye"`))
		publisher.Flush()
		r := helpers.RequireValue(t, requestsCh, time.Second)
		uncompressed, err := util.DecompressGzipData(r.Body)
		assert.NoError(t, err)
		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["hello"]`))
	})
}

func TestHTTPEventPublisherErrorRetry(t *testing.T) {
	testRecoverableError := func(t *testing.T, errorHandler http.Handler) {
		mockLog := ldlogtest.NewMockLog()
		defer mockLog.DumpIfTestFailed(t)
		successHandler := httphelpers.HandlerWithStatus(202)
		handler, requestsCh := httphelpers.RecordingHandler(
			httphelpers.SequentialHandler(errorHandler, errorHandler, successHandler),
		)
		httphelpers.WithServer(handler, func(server *httptest.Server) {
			publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(),
				OptionBaseURI(server.URL))
			defer publisher.Close()
			publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
			timeStart := time.Now()
			publisher.Flush()
			req1 := helpers.RequireValue(t, requestsCh, time.Second*5)
			uncompressed1, err := util.DecompressGzipData(req1.Body)
			assert.NoError(t, err)

			req2 := helpers.RequireValue(t, requestsCh, time.Second*5)
			uncompressed2, err := util.DecompressGzipData(req2.Body)
			assert.NoError(t, err)

			elapsed := time.Since(timeStart)
			assert.Equal(t, []byte(`["hello"]`), uncompressed1)
			assert.Equal(t, uncompressed1, uncompressed2)
			assert.GreaterOrEqual(t, int64(elapsed), int64(time.Second))

			// There were two failures, so it should not have retried again after that (should not reach successHandler)
			helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50)
		})
	}

	t.Run("HTTP 503", func(t *testing.T) {
		testRecoverableError(t, httphelpers.HandlerWithStatus(503))
	})

	t.Run("network error", func(t *testing.T) {
		testRecoverableError(t, httphelpers.BrokenConnectionHandler())
	})
}

func TestHTTPEventPublisherUnrecoverableError(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(401))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()
		_ = helpers.RequireValue(t, requestsCh, time.Second)
		time.Sleep(time.Millisecond * 100) // no good way to know when it's processed the 401 response
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()
		helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50)
	})
}

func TestHTTPEventPublisherUnrecoverableErrorDoesNotBlockFutureProcessing(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(401))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()

		_ = helpers.RequireValue(t, requestsCh, time.Second)
		time.Sleep(time.Millisecond * 100) // no good way to know when it's processed the 401 response

		// Make sure the queue hasn't stopped processing by overfilling its capacity. This shouldn't block.
		for i := 0; i < inputQueueSize+1; i++ {
			publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"into the void!"`))
		}
		publisher.Flush()
		helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*50)
	})
}

func TestHTTPEventPublisherReplaceCredential(t *testing.T) {
	newSDKKey := config.SDKKey("better-sdk-key")
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(), OptionBaseURI(server.URL))
		defer publisher.Close()

		publisher.ReplaceCredential(newSDKKey)
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()

		r1 := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, string(newSDKKey), r1.Request.Header.Get("Authorization"))

		// Providing a new MobileKey when this publisher is currently using an SDKKey has no effect
		publisher.ReplaceCredential(config.MobileKey("ignore-this"))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()

		r2 := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, string(newSDKKey), r2.Request.Header.Get("Authorization"))
	})
}

func TestInitialQueueCapacity(t *testing.T) {
	// Unset initial capacity preallocates the full capacity -- the original behavior, used by the
	// analytics publisher, which never sets OptionInitialCapacity.
	assert.Equal(t, 1000, initialQueueCapacity(1000, 0))
	assert.Equal(t, 10000, initialQueueCapacity(10000, 0))
	// A smaller initial capacity is used as-is, letting the queue start small and grow.
	assert.Equal(t, 1000, initialQueueCapacity(10000, 1000))
	// The initial allocation is never larger than the maximum capacity.
	assert.Equal(t, 1000, initialQueueCapacity(1000, 1000))
	assert.Equal(t, 1000, initialQueueCapacity(1000, 5000))
}

func TestHTTPEventPublisherInitialCapacityGrowsToCapacity(t *testing.T) {
	// With an initial capacity smaller than the (maximum) capacity, the queue must still grow past
	// the initial allocation and only drop events once the maximum capacity is reached.
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), slog.Default(),
			OptionBaseURI(server.URL), OptionCapacity(3), OptionInitialCapacity(1))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"a"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"b"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"c"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"d"`))
		publisher.Flush()
		r := helpers.RequireValue(t, requestsCh, time.Second)

		uncompressed, err := util.DecompressGzipData(r.Body)
		assert.NoError(t, err)

		// The queue grew from the initial capacity of 1 up to the capacity of 3, then dropped "d".
		m.In(t).Assert(uncompressed, m.JSONStrEqual(`["a","b","c"]`))
	})
}
