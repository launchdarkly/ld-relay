package relay

// This file contains test helpers for dealing with HTTP requests.

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
)

// Simple shortcut for creating a request that may or may not have a body.
func buildRequest(method, url string, body []byte, headers http.Header) *http.Request {
	var bodyBuffer io.Reader
	if body != nil {
		bodyBuffer = bytes.NewBuffer(body)
	}
	r, err := http.NewRequest(method, url, bodyBuffer)
	if err != nil {
		panic(err)
	}
	r.Header = headers
	return r
}

func addQueryParam(url, query string) string {
	if strings.Contains(url, "?") {
		return url + "&" + query
	}
	return url + "?" + query
}

// Shortcut for building a request when we are going to be passing it directly to an endpoint handler, rather than
// going through the usual routing mechanism, so we must provide the Context and the URL path variables explicitly.
func buildPreRoutedRequest(verb string, body []byte, headers http.Header, vars map[string]string, ctx relayenv.EnvContext) *http.Request {
	req := buildRequest(verb, "", body, headers)
	req = mux.SetURLVars(req, vars)
	req = req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: ctx}))
	return req
}

// Shortcut for executing an endpoint handler against a request and getting the response.
func doRequest(req *http.Request, handler http.Handler) (*http.Response, []byte) {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	result := w.Result()
	body, _ := ioutil.ReadAll(result.Body)
	return result, body
}

func doStreamRequestExpectingError(req *http.Request, handler http.Handler) *http.Response {
	w, bodyReader := sharedtest.NewStreamRecorder()
	handler.ServeHTTP(w, req)
	go func() {
		_, _ = ioutil.ReadAll(bodyReader)
	}()
	return w.Result()
}

func nullHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
}

type bodyMatcher func(t *testing.T, body []byte)

func expectBody(expectedBody string) bodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.EqualValues(t, expectedBody, body)
	}
}

func expectJSONBody(expectedBody string) bodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.JSONEq(t, expectedBody, string(body))
	}
}

func expectJSONEntity(entity interface{}) bodyMatcher {
	bytes, _ := json.Marshal(entity)
	return expectJSONBody(string(bytes))
}

func assertNonStreamingHeaders(t *testing.T, h http.Header) {
	assert.Equal(t, "", h.Get("X-Accel-Buffering"))
	assert.NotRegexp(t, "^text/event-stream", h.Get("Content-Type"))
}

func assertStreamingHeaders(t *testing.T, h http.Header) {
	assert.Equal(t, "no", h.Get("X-Accel-Buffering"))
	assert.Regexp(t, "^text/event-stream", h.Get("Content-Type"))
}

// Standard test logic for our standard handling of OPTIONS preflight requests.
func assertEndpointSupportsOptionsRequest(
	t *testing.T,
	handler http.Handler,
	url, usualMethod string,
) {
	host := "my-host.com"

	r1, _ := http.NewRequest("OPTIONS", url, nil)
	result1, _ := doRequest(r1, handler)
	if assert.Equal(t, http.StatusOK, result1.StatusCode) {
		assertExpectedCORSHeaders(t, result1, usualMethod, "*")
	}

	r2, _ := http.NewRequest("OPTIONS", url, nil)
	r2.Header.Set("Origin", host)
	result2, _ := doRequest(r2, handler)
	if assert.Equal(t, http.StatusOK, result2.StatusCode) {
		assertExpectedCORSHeaders(t, result2, usualMethod, host)
	}
}

func assertExpectedCORSHeaders(t *testing.T, resp *http.Response, endpointMethod string, host string) {
	assert.ElementsMatch(t, []string{endpointMethod, "OPTIONS", "OPTIONS"},
		strings.Split(resp.Header.Get("Access-Control-Allow-Methods"), ","))
	assert.Equal(t, host, resp.Header.Get("Access-Control-Allow-Origin"))
}

// Makes a request that should receive an SSE stream, and calls the given code with a channel that
// will read from that stream. A nil value is pushed to the channel when the stream closes or
// encounters an error.
func withStreamRequest(
	t *testing.T,
	req *http.Request,
	handler http.Handler,
	action func(<-chan eventsource.Event),
) *http.Response {
	resp := sharedtest.WithStreamRequest(t, req, handler, action)
	if resp != nil {
		assertStreamingHeaders(t, resp.Header)
	}
	return resp
}
