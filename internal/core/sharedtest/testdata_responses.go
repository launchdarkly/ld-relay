package sharedtest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type BodyMatcher func(t *testing.T, body []byte)

func ExpectBody(expectedBody string) BodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.EqualValues(t, expectedBody, body)
	}
}

func ExpectJSONBody(expectedBody string) BodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.JSONEq(t, expectedBody, string(body))
	}
}

func ExpectJSONEntity(entity interface{}) BodyMatcher {
	bytes, _ := json.Marshal(entity)
	return ExpectJSONBody(string(bytes))
}

func AssertNonStreamingHeaders(t *testing.T, h http.Header) {
	assert.Equal(t, "", h.Get("X-Accel-Buffering"))
	assert.NotRegexp(t, "^text/event-stream", h.Get("Content-Type"))
}

func AssertStreamingHeaders(t *testing.T, h http.Header) {
	AssertStreamingContentType(t, h)
	assert.Equal(t, "no", h.Get("X-Accel-Buffering"))
}

func AssertStreamingContentType(t *testing.T, h http.Header) {
	assert.Regexp(t, "^text/event-stream", h.Get("Content-Type"))
}

func AssertEndpointSupportsOptionsRequest(
	t *testing.T,
	handler http.Handler,
	url, usualMethod string,
) {
	host := "my-host.com"

	r1, _ := http.NewRequest("OPTIONS", url, nil)
	result1, _ := DoRequest(r1, handler)
	if assert.Equal(t, http.StatusOK, result1.StatusCode) {
		AssertExpectedCORSHeaders(t, result1, usualMethod, "*")
	}

	r2, _ := http.NewRequest("OPTIONS", url, nil)
	r2.Header.Set("Origin", host)
	result2, _ := DoRequest(r2, handler)
	if assert.Equal(t, http.StatusOK, result2.StatusCode) {
		AssertExpectedCORSHeaders(t, result2, usualMethod, host)
	}
}

func AssertExpectedCORSHeaders(t *testing.T, resp *http.Response, endpointMethod string, host string) {
	assert.ElementsMatch(t, []string{endpointMethod, "OPTIONS"},
		strings.Split(resp.Header.Get("Access-Control-Allow-Methods"), ","))
	assert.Equal(t, host, resp.Header.Get("Access-Control-Allow-Origin"))
}

func MakeEvalBody(flags []TestFlag, fullData bool, reasons bool) string {
	obj := make(map[string]interface{})
	for _, f := range flags {
		value := f.ExpectedValue
		if fullData {
			m := map[string]interface{}{"value": value, "version": f.Flag.Version}
			if value != nil {
				m["variation"] = f.ExpectedVariation
			} else {
				m["variation"] = nil
			}
			if reasons || f.IsExperiment {
				m["reason"] = f.ExpectedReason
			}
			if f.Flag.TrackEvents || f.IsExperiment {
				m["trackEvents"] = true
			}
			if f.IsExperiment {
				m["trackReason"] = true
			}
			value = m
		}
		obj[f.Flag.Key] = value
	}
	out, _ := json.Marshal(obj)
	return string(out)
}
