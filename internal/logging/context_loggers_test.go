package logging

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"

	"github.com/stretchr/testify/assert"
)

func TestGlobalContextLoggers(t *testing.T) {
	assert.Equal(t, slog.Default(), GetContextLogger(context.Background()))

	logger, _ := logtest.NewMockLogger()
	req, _ := http.NewRequest("GET", "", nil)
	ContextLoggerMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, logger, GetContextLogger(r.Context()))
	})).ServeHTTP(&httptest.ResponseRecorder{}, req)
}
