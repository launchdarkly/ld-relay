package browser

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type mockCORSContext struct{}

func (m mockCORSContext) AllowedOrigins() []string {
	return nil
}

func (m mockCORSContext) AllowedHeaders() []string {
	return nil
}

func TestCORSContext(t *testing.T) {
	t.Run("GetCORSContext when there is no RequestContext returns nil", func(t *testing.T) {
		assert.Nil(t, GetCORSContext(context.Background()))
	})

	t.Run("WithCORSContext adds RequestContext to context", func(t *testing.T) {
		m := mockCORSContext{}
		ctx := WithCORSContext(context.Background(), m)
		assert.Equal(t, m, GetCORSContext(ctx))
	})

	t.Run("WithCORSContext has no effect with nil parameter", func(t *testing.T) {
		ctx := WithCORSContext(context.Background(), nil)
		assert.Equal(t, context.Background(), ctx)
	})

	t.Run("SetCORSHeaders", func(t *testing.T) {
		origin := "http://good.cat"
		rr := httptest.ResponseRecorder{}
		SetCORSHeaders(&rr, origin, nil)
		assert.Equal(t, origin, rr.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "false", rr.Header().Get("Access-Control-Allow-Credentials"))
		assert.Equal(t, maxAge, rr.Header().Get("Access-Control-Max-Age"))
		assert.Equal(t, DefaultAllowedHeaders, rr.Header().Get("Access-Control-Allow-Headers"))
		assert.Equal(t, "Date", rr.Header().Get("Access-Control-Expose-Headers"))
	})

	t.Run("SetCORSHeaders with additionalHeaders", func(t *testing.T) {
		origin := "http://good.cat"
		rr := httptest.ResponseRecorder{}
		extraHeaders := []string{"Toast", "Bread"}
		expectedHeaders := strings.Join([]string{DefaultAllowedHeaders, "Toast,Bread"}, ",")
		SetCORSHeaders(&rr, origin, extraHeaders)
		assert.Equal(t, origin, rr.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "false", rr.Header().Get("Access-Control-Allow-Credentials"))
		assert.Equal(t, maxAge, rr.Header().Get("Access-Control-Max-Age"))
		assert.Equal(t, expectedHeaders, rr.Header().Get("Access-Control-Allow-Headers"))
		assert.Equal(t, "Date", rr.Header().Get("Access-Control-Expose-Headers"))
	})

	t.Run("SetCORSHeaders sets Vary: Origin", func(t *testing.T) {
		rr := httptest.ResponseRecorder{}
		SetCORSHeaders(&rr, "http://good.cat", nil)
		assert.Equal(t, []string{"Origin"}, rr.Header().Values("Vary"))
	})

	t.Run("SetCORSHeaders preserves an existing Vary value", func(t *testing.T) {
		rr := httptest.ResponseRecorder{}
		rr.Header().Set("Vary", "Authorization")
		SetCORSHeaders(&rr, "http://good.cat", nil)
		assert.Equal(t, []string{"Authorization", "Origin"}, rr.Header().Values("Vary"))
	})
}

func TestAddVaryHeader(t *testing.T) {
	t.Run("appends to an existing comma-separated value", func(t *testing.T) {
		rr := httptest.ResponseRecorder{}
		rr.Header().Set("Vary", "Authorization, Accept-Encoding")
		AddVaryHeader(&rr, "Origin")
		assert.Equal(t, []string{"Authorization, Accept-Encoding", "Origin"}, rr.Header().Values("Vary"))
	})

	t.Run("does not duplicate a value that is already present", func(t *testing.T) {
		rr := httptest.ResponseRecorder{}
		rr.Header().Set("Vary", "Authorization, origin")
		AddVaryHeader(&rr, "Origin")
		assert.Equal(t, []string{"Authorization, origin"}, rr.Header().Values("Vary"))
	})
}
