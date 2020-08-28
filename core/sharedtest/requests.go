package sharedtest

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
)

// BuildRequest is a simple shortcut for creating a request that may or may not have a body.
func BuildRequest(method, url string, body []byte, headers http.Header) *http.Request {
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

// AddQueryParam is a shortcut for concatenating a query string to a URL that may or may not have one.
func AddQueryParam(url, query string) string {
	if strings.Contains(url, "?") {
		return url + "&" + query
	}
	return url + "?" + query
}

// DoRequest is a shortcut for executing an endpoint handler against a request and getting the response.
func DoRequest(req *http.Request, handler http.Handler) (*http.Response, []byte) {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	result := w.Result()
	var body []byte
	if result.Body != nil {
		body, _ = ioutil.ReadAll(result.Body)
		_ = result.Body.Close()
	}

	return result, body
}
