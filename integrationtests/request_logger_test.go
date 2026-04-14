//go:build integrationtests

package integrationtests

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
)

type requestLogger struct {
	transport http.RoundTripper
	enabled   bool
	logger    *slog.Logger
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
	r.logger.Info("request", "method", request.Method, "url", request.URL)
	r.logger.Info("request headers", "headers", request.Header)
	if request.Body != nil {
		bodyCopy := copyBody(&request.Body)
		if len(bodyCopy) != 0 {
			r.logger.Info("request body", "body", string(bodyCopy))
		}
	}
}

func (r *requestLogger) logResponse(resp *http.Response, withBody bool) {
	if !r.enabled || resp == nil {
		return
	}
	r.logger.Info("response", "status", resp.StatusCode)
	r.logger.Info("response headers", "headers", resp.Header)
	if withBody && resp.Body != nil {
		bodyCopy := copyBody(&resp.Body)
		if len(bodyCopy) != 0 {
			r.logger.Info("response body", "body", string(bodyCopy))
		}
	}
}

func copyBody(body *io.ReadCloser) []byte {
	bodyCopy := bytes.NewBuffer(nil)
	io.Copy(bodyCopy, *body)
	(*body).Close()
	*body = io.NopCloser(bodyCopy)
	return bodyCopy.Bytes()
}
