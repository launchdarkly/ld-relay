// +build integrationtests

package integrationtests

import (
	"bytes"
	"io"
	"io/ioutil"
	"net/http"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type requestLogger struct {
	transport http.RoundTripper
	enabled   bool
	loggers   ldlog.Loggers
}

func (r *requestLogger) RoundTrip(request *http.Request) (*http.Response, error) {
	r.logRequest(request)
	resp, err := r.transport.RoundTrip(request)
	r.logResponse(resp, true)
	return resp, err
}

func (r *requestLogger) logRequest(request *http.Request) {
	if !r.enabled || request == nil {
		return
	}
	r.loggers.Infof("%s %s", request.Method, request.URL)
	r.loggers.Infof("    headers: %s", request.Header)
	if request.Body != nil {
		bodyCopy := copyBody(&request.Body)
		if len(bodyCopy) != 0 {
			r.loggers.Infof("    body: %s", string(bodyCopy))
		}
	}
}

func (r *requestLogger) logResponse(resp *http.Response, withBody bool) {
	if !r.enabled || resp == nil {
		return
	}
	r.loggers.Infof("  response status %d", resp.StatusCode)
	r.loggers.Infof("    headers: %s", resp.Header)
	if withBody && resp.Body != nil {
		bodyCopy := copyBody(&resp.Body)
		if len(bodyCopy) != 0 {
			r.loggers.Infof("    body: %s", string(bodyCopy))
		}
	}
}

func copyBody(body *io.ReadCloser) []byte {
	bodyCopy := bytes.NewBuffer(nil)
	io.Copy(bodyCopy, *body)
	(*body).Close()
	*body = ioutil.NopCloser(bodyCopy)
	return bodyCopy.Bytes()
}
