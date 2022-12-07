package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

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
