package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"

	"github.com/stretchr/testify/assert"
)

func TestGlobalContextLoggers(t *testing.T) {
	assert.Equal(t, ldlog.NewDisabledLoggers(), GetGlobalContextLoggers(context.Background()))

	mockLog := ldlogtest.NewMockLog()
	req, _ := http.NewRequest("GET", "", nil)
	GlobalContextLoggersMiddleware(mockLog.Loggers)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, mockLog.Loggers, GetGlobalContextLoggers(r.Context()))
	})).ServeHTTP(&httptest.ResponseRecorder{}, req)
}
