package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamAdmissionControllerUnlimited(t *testing.T) {
	c := NewStreamAdmissionController(0)
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
	c := NewStreamAdmissionController(2)

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
	handler.ServeHTTP(rejectedRec, httptest.NewRequest("GET", "/all", nil))
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
