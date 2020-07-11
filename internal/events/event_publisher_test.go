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
	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func TestEventPublisher(t *testing.T) {
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHttpEventPublisher(config.SDKKey("my-key"), ldlog.NewDisabledLoggers(), OptionUri(server.URL))
		defer publisher.Close()
		publisher.Publish("hello")
		publisher.Publish("hello again")
		publisher.Flush()
		select {
		case <-time.After(time.Second):
			assert.Fail(t, "timed out")
		case r := <-requestsCh:
			assert.Equal(t, "/bulk", r.Request.URL.Path)
			assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
			assert.JSONEq(t, `["hello", "hello again"]`, string(r.Body))
		}
	})
}

func TestEventPublishRaw(t *testing.T) {
	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		publisher, _ := NewHttpEventPublisher(config.SDKKey("my-key"), ldlog.NewDisabledLoggers(), OptionUri(server.URL))
		defer publisher.Close()
		publisher.PublishRaw(json.RawMessage(`{"hello": 1}`))
		publisher.Flush()
		select {
		case <-time.After(time.Second):
			assert.Fail(t, "timed out")
		case r := <-requestsCh:
			assert.Equal(t, "/bulk", r.Request.URL.Path)
			assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), r.Request.Header.Get(EventSchemaHeader))
			assert.JSONEq(t, `[{"hello": 1}]`, string(r.Body))
		}
	})
}

func TestEventPublisherClosesImmediatelyAndOnlyOnce(t *testing.T) {
	publisher, _ := NewHttpEventPublisher(config.SDKKey("my-key"), ldlog.NewDisabledLoggers())
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
	publisher, _ := NewHttpEventPublisher(config.SDKKey("my-key"), ldlog.NewDisabledLoggers(), OptionUri(server.URL), OptionFlushInterval(time.Millisecond))
	defer publisher.Close()
	publisher.Publish("hello")
	select {
	case <-time.After(time.Second):
		assert.Fail(t, "timed out")
	case data := <-body:
		assert.JSONEq(t, `["hello"]`, string(data))
	}
}

func TestHttpEventPublisherCapacity(t *testing.T) {
	body := make(chan []byte)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "/bulk", req.URL.Path)
		assert.Equal(t, strconv.Itoa(SummaryEventsSchemaVersion), req.Header.Get(EventSchemaHeader))
		data, _ := ioutil.ReadAll(req.Body)
		body <- data
	}))
	publisher, _ := NewHttpEventPublisher(config.SDKKey("my-key"), ldlog.NewDisabledLoggers(), OptionUri(server.URL), OptionCapacity(1))
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
