package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTestEnvContext attaches a minimal, real EnvContext to the request so that
// recordStreamAdmissionRejected's call to GetEnvContextInfo doesn't panic, matching how
// requests reach these handlers in production (after the SDK-key-selecting middleware runs).
func withTestEnvContext(t *testing.T, req *http.Request) *http.Request {
	env, err := relayenv.NewEnvContext(relayenv.EnvContextImplParams{
		Identifiers:   relayenv.EnvIdentifiers{ConfiguredName: "testenv"},
		EnvConfig:     config.EnvConfig{},
		AllConfig:     config.Config{},
		ClientFactory: testclient.FakeLDClientFactory(true),
		Loggers:       ldlogtest.NewMockLog().Loggers,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Close() })
	return req.WithContext(WithEnvContextInfo(req.Context(), EnvContextInfo{Env: env}))
}

func TestStreamAdmissionControllerUnlimited(t *testing.T) {
	c := &StreamAdmissionController{limit: 0}
	handler := c.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/all", nil))
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestStreamAdmissionControllerRejectsOverLimit(t *testing.T) {
	c := &StreamAdmissionController{limit: 2}

	// block indefinitely until released, simulating a held-open streaming connection
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	handler := c.Limit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	// admit exactly 2 concurrent, blocking connections
	var wg sync.WaitGroup
	admitted := make([]*httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", "/all", nil))
			admitted[idx] = rec
		}(i)
	}
	<-started
	<-started
	require.Equal(t, int64(2), c.ActiveStreams())

	// with the strict ordering guarantee below (this call happens-after both admissions
	// and happens-before release is closed), a 3rd request must be rejected deterministically
	rejectedRec := httptest.NewRecorder()
	rejectedReq := withTestEnvContext(t, httptest.NewRequest("GET", "/all", nil))
	handler.ServeHTTP(rejectedRec, rejectedReq)
	assert.Equal(t, http.StatusServiceUnavailable, rejectedRec.Code)
	assert.Equal(t, "5", rejectedRec.Header().Get("Retry-After"))
	assert.Equal(t, int64(2), c.ActiveStreams(), "rejected request must not count against the limit")

	close(release)
	wg.Wait()

	for _, rec := range admitted {
		assert.Equal(t, http.StatusOK, rec.Code)
	}
	assert.Equal(t, int64(0), c.ActiveStreams(), "active count should return to zero after all handlers complete")
}

func TestAutoDetectStreamLimitBounds(t *testing.T) {
	assert.GreaterOrEqual(t, autoDetectStreamLimit(), 0, "should never return a negative limit")
}
