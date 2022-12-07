package sharedtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	m "github.com/launchdarkly/go-test-helpers/v3/matchers"

	"github.com/stretchr/testify/assert"
)

func ExpectBody(expectedBody string) m.Matcher {
	return m.Equal([]byte(expectedBody))
}

func ExpectJSONBody(expectedBody string) m.Matcher {
	fmt.Println(expectedBody)
	return m.JSONStrEqual(expectedBody)
}

func ExpectJSONEntity(entity interface{}) m.Matcher {
	return m.JSONEqual(entity)
}

func ExpectNoBody() m.Matcher {
	return m.Length().Should(m.Equal(0)) // works for either nil body or empty []byte{}
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

func MakeEvalBody(flags []TestFlag, reasons bool) string {
	obj := make(map[string]interface{})
	for _, f := range flags {
		value := f.ExpectedValue
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
		obj[f.Flag.Key] = m
	}
	out, _ := json.Marshal(obj)
	return string(out)
}
