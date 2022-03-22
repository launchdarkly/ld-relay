package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	m "github.com/launchdarkly/go-test-helpers/v2/matchers"

	"github.com/stretchr/testify/assert"
)

const testSDKKey = config.SDKKey("my-key")

func defaultHTTPConfig() httpconfig.HTTPConfig {
	hc, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDisabledLoggers())
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
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), mockLog.Loggers, OptionURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello again"`))
		publisher.Flush()
		r := st.ExpectTestRequest(t, requestsCh, time.Second)
		assert.Equal(t, "/bulk", r.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
		m.In(t).Assert(r.Body, m.JSONStrEqual(`["hello", "hello again"]`))
	})
}

func TestHTTPEventPublisherMultiQueuesWithMetadata(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), mockLog.Loggers, OptionURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{Tags: "a"}, json.RawMessage(`"hello"`))
		publisher.Publish(EventPayloadMetadata{Tags: "b"}, json.RawMessage(`"ok"`))
		publisher.Publish(EventPayloadMetadata{Tags: "a"}, json.RawMessage(`"hello again"`))
		publisher.Publish(EventPayloadMetadata{Tags: "b"}, json.RawMessage(`"thanks"`))
		publisher.Flush()
		r1 := st.ExpectTestRequest(t, requestsCh, time.Second)
		r2 := st.ExpectTestRequest(t, requestsCh, time.Second)
		if r1.Request.Header.Get(TagsHeader) == "b" {
			r2, r1 = r1, r2
		}
		assert.Equal(t, "/bulk", r1.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r1.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r1.Request.Header.Get(EventSchemaHeader))
		assert.Equal(t, "a", r1.Request.Header.Get(TagsHeader))
		m.In(t).Assert(r1.Body, m.JSONStrEqual(`["hello", "hello again"]`))

		assert.Equal(t, "/bulk", r2.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r2.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r2.Request.Header.Get(EventSchemaHeader))
		assert.Equal(t, "b", r2.Request.Header.Get(TagsHeader))
		m.In(t).Assert(r2.Body, m.JSONStrEqual(`["ok", "thanks"]`))
	})
}

func TestHTTPEventPublisherOptionEndpointURI(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), mockLog.Loggers,
			OptionEndpointURI(server.URL+"/special-path"))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()
		r := st.ExpectTestRequest(t, requestsCh, time.Second)
		assert.Equal(t, "/special-path", r.Request.URL.Path)
		assert.Equal(t, string(testSDKKey), r.Request.Header.Get("Authorization"))
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
		m.In(t).Assert(r.Body, m.JSONStrEqual(`["hello"]`))
	})
}

func TestHTTPEventPublisherClosesImmediatelyAndOnlyOnce(t *testing.T) {
	publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), ldlog.NewDisabledLoggers())
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
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), mockLog.Loggers,
			OptionURI(server.URL), OptionFlushInterval(time.Millisecond))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		r := st.ExpectTestRequest(t, requestsCh, time.Second)
		assert.Equal(t, "/bulk", r.Request.URL.Path)
		m.In(t).Assert(r.Body, m.JSONStrEqual(`["hello"]`))
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
	})
}

func TestHTTPEventPublisherFlushDoesNothingIfThereAreNoEvents(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), mockLog.Loggers,
			OptionURI(server.URL), OptionFlushInterval(time.Millisecond))
		defer publisher.Close()
		publisher.Flush()
		st.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)
	})
}

func TestHTTPEventPublisherCapacity(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), mockLog.Loggers,
			OptionURI(server.URL), OptionCapacity(1))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"goodbye"`))
		publisher.Flush()
		r := st.ExpectTestRequest(t, requestsCh, time.Second)
		assert.Equal(t, "/bulk", r.Request.URL.Path)
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
		m.In(t).Assert(r.Body, m.JSONStrEqual(`["hello"]`))
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
			publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), mockLog.Loggers,
				OptionURI(server.URL))
			defer publisher.Close()
			publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
			timeStart := time.Now()
			publisher.Flush()
			req1 := <-requestsCh
			req2 := <-requestsCh
			elapsed := time.Since(timeStart)
			assert.Equal(t, []byte(`["hello"]`), req1.Body)
			assert.Equal(t, req1.Body, req2.Body)
			assert.GreaterOrEqual(t, int64(elapsed), int64(time.Second))

			// There were two failures, so it should not have retried again after that (should not reach successHandler)
			st.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)
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
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), mockLog.Loggers,
			OptionURI(server.URL))
		defer publisher.Close()
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()
		<-requestsCh
		time.Sleep(time.Millisecond * 100) // no good way to know when it's processed the 401 response
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()
		st.ExpectNoTestRequests(t, requestsCh, time.Millisecond*50)
	})
}

func TestHTTPEventPublisherReplaceCredential(t *testing.T) {
	newSDKKey := config.SDKKey("better-sdk-key")
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), mockLog.Loggers, OptionURI(server.URL))
		defer publisher.Close()

		publisher.ReplaceCredential(newSDKKey)
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()

		r1 := st.ExpectTestRequest(t, requestsCh, time.Second)
		assert.Equal(t, string(newSDKKey), r1.Request.Header.Get("Authorization"))

		// Providing a new MobileKey when this publisher is currently using an SDKKey has no effect
		publisher.ReplaceCredential(config.MobileKey("ignore-this"))
		publisher.Publish(EventPayloadMetadata{}, json.RawMessage(`"hello"`))
		publisher.Flush()

		r2 := st.ExpectTestRequest(t, requestsCh, time.Second)
		assert.Equal(t, string(newSDKKey), r2.Request.Header.Get("Authorization"))
	})
}
