package events

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/httpconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const testSDKKey = config.SDKKey("my-key")

func defaultHTTPConfig() httpconfig.HTTPConfig {
	hc, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDisabledLoggers())
	if err != nil {
		panic(err)
	}
	return hc
}

func TestEventPublisher(t *testing.T) {
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), ldlog.NewDisabledLoggers(), OptionURI(server.URL))
		defer publisher.Close()
		publisher.Publish("hello")
		publisher.Publish("hello again")
		publisher.Flush()
		select {
		case <-time.After(time.Second):
			assert.Fail(t, "timed out")
		case r := <-requestsCh:
			assert.Equal(t, "/bulk", r.Request.URL.Path)
			assert.Equal(t, string(testSDKKey), r.Request.Header.Get("Authorization"))
			assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
			assert.JSONEq(t, `["hello", "hello again"]`, string(r.Body))
		}
	})
}

func TestEventPublishRaw(t *testing.T) {
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), ldlog.NewDisabledLoggers(), OptionURI(server.URL))
		defer publisher.Close()
		publisher.PublishRaw(json.RawMessage(`{"hello": 1}`))
		publisher.Flush()
		select {
		case <-time.After(time.Second):
			assert.Fail(t, "timed out")
		case r := <-requestsCh:
			assert.Equal(t, "/bulk", r.Request.URL.Path)
			assert.Equal(t, string(testSDKKey), r.Request.Header.Get("Authorization"))
			assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
			assert.JSONEq(t, `[{"hello": 1}]`, string(r.Body))
		}
	})
}

func TestEventPublisherClosesImmediatelyAndOnlyOnce(t *testing.T) {
	publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), ldlog.NewDisabledLoggers())
	timeout := time.After(time.Second)
	publisher.Close()
	publisher.Close()
	assert.Len(t, timeout, 0, "expected timeout to not have triggered but it did")
}

func TestPublisherAutomaticFlush(t *testing.T) {
	body := make(chan []byte)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/bulk", req.URL.Path)
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), req.Header.Get(EventSchemaHeader))
		data, _ := ioutil.ReadAll(req.Body)
		body <- data
	}))
	publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), ldlog.NewDisabledLoggers(),
		OptionURI(server.URL), OptionFlushInterval(time.Millisecond))
	defer publisher.Close()
	publisher.Publish("hello")
	select {
	case <-time.After(time.Second):
		assert.Fail(t, "timed out")
	case data := <-body:
		assert.JSONEq(t, `["hello"]`, string(data))
	}
}

func TestHTTPEventPublisherCapacity(t *testing.T) {
	body := make(chan []byte)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/bulk", req.URL.Path)
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), req.Header.Get(EventSchemaHeader))
		data, _ := ioutil.ReadAll(req.Body)
		body <- data
	}))
	publisher, _ := NewHTTPEventPublisher(config.SDKKey("my-key"), defaultHTTPConfig(), ldlog.NewDisabledLoggers(),
		OptionURI(server.URL), OptionCapacity(1))
	defer publisher.Close()
	publisher.Publish("hello")
	publisher.Publish("goodbye")
	publisher.Flush()
	select {
	case <-time.After(time.Second):
		assert.Fail(t, "timed out")
	case data := <-body:
		assert.JSONEq(t, `["hello"]`, string(data))
	}
}

func TestHTTPEventPublisherErrorRetry(t *testing.T) {
	testRecoverableError := func(t *testing.T, errorHandler http.Handler) {
		successHandler := httphelpers.HandlerWithStatus(202)
		handler, requestsCh := httphelpers.RecordingHandler(
			httphelpers.SequentialHandler(errorHandler, errorHandler, successHandler),
		)
		httphelpers.WithServer(handler, func(server *httptest.Server) {
			publisher, _ := NewHTTPEventPublisher(testSDKKey, defaultHTTPConfig(), ldlog.NewDisabledLoggers(),
				OptionURI(server.URL))
			defer publisher.Close()
			publisher.Publish("hello")
			timeStart := time.Now()
			publisher.Flush()
			req1 := <-requestsCh
			req2 := <-requestsCh
			elapsed := time.Since(timeStart)
			assert.Equal(t, []byte(`["hello"]`), req1.Body)
			assert.Equal(t, req1.Body, req2.Body)
			assert.GreaterOrEqual(t, int64(elapsed), int64(time.Second))

			// There were two failures, so it should not have retried again after that (should not reach successHandler)
			select {
			case <-requestsCh:
				assert.Fail(t, "request was unexpectedly tried a third time")
			case <-time.After(time.Millisecond * 50):
				break
			}
		})
	}

	t.Run("HTTP 503", func(t *testing.T) {
		testRecoverableError(t, httphelpers.HandlerWithStatus(503))
	})

	t.Run("network error", func(t *testing.T) {
		testRecoverableError(t, httphelpers.BrokenConnectionHandler())
	})
}
