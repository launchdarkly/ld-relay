package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimitConcurrencyDisabledIsPassThrough(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 0}) // disabled
	called := false
	h := LimitConcurrency(limiter, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/sdk/flags", nil))
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestLimitConcurrencyShedsWhenBudgetFull(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	// Occupy the only slot so the wrapped request must shed.
	release, ok := limiter.Acquire(context.Background())
	require.True(t, ok)
	defer release()

	called := false
	h := LimitConcurrency(limiter, time.Second)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/sdk/flags", nil))

	assert.False(t, called, "handler must not run when shed")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Body.String(), "concurrency limit reached")

	// Retry-After is a small positive integer, jittered into a range so a shed herd does not
	// retry in lockstep.
	ra, err := strconv.Atoi(rr.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, ra, 2)
	assert.LessOrEqual(t, ra, 5)
}

func TestLimitConcurrencyReleasesSlotWhenHandlerReturns(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	h := LimitConcurrency(limiter, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Two sequential requests both succeed, which is only possible if the single slot is
	// released when each handler returns.
	for range 2 {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", "/sdk/flags", nil))
		assert.Equal(t, http.StatusOK, rr.Code)
	}
	assert.Equal(t, int64(2), limiter.Stats().Admitted)
}

func TestAcquireInitSlotFromContextChargesOnlyWhenProvided(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})

	// With no limiter installed in the context, callers are admitted with a no-op release and
	// the budget is untouched. This is what lets an FDv2 up-to-date reply avoid a slot.
	rr := httptest.NewRecorder()
	release, ok := AcquireInitSlotFromContext(rr, httptest.NewRequest("GET", "/sdk/poll", nil))
	require.True(t, ok)
	release()
	assert.Equal(t, int64(0), limiter.Stats().Admitted, "absent limiter must not charge a slot")

	// When ProvideInitLimiter installed the limiter, a full-basis handler charges a slot.
	provided := ProvideInitLimiter(limiter, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, ok := AcquireInitSlotFromContext(w, r)
		require.True(t, ok)
		defer rel()
		w.WriteHeader(http.StatusOK)
	}))
	rr2 := httptest.NewRecorder()
	provided.ServeHTTP(rr2, httptest.NewRequest("GET", "/sdk/poll", nil))
	assert.Equal(t, http.StatusOK, rr2.Code)
	assert.Equal(t, int64(1), limiter.Stats().Admitted)
}

func TestAcquireInitSlotFromContextShedsWhenBudgetFull(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	release, ok := limiter.Acquire(context.Background())
	require.True(t, ok)
	defer release()

	handlerRan := false
	provided := ProvideInitLimiter(limiter, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, ok := AcquireInitSlotFromContext(w, r)
		if !ok {
			return
		}
		defer rel()
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	provided.ServeHTTP(rr, httptest.NewRequest("GET", "/sdk/poll", nil))

	assert.False(t, handlerRan, "full-basis handler must not proceed once shed")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
