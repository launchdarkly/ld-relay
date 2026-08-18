package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimitConcurrencyDisabledIsPassThrough(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 0}) // disabled
	called := false
	h := LimitConcurrency(limiter, time.Second, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	h := LimitConcurrency(limiter, time.Second, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/sdk/flags", nil))

	assert.False(t, called, "handler must not run when shed")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Body.String(), "concurrency limit reached")

	// The shed reply names no retry time. The SDKs pace their own retries with
	// exponential backoff and jitter.
	assert.Empty(t, rr.Header().Get("Retry-After"))
}

func TestLimitConcurrencyReleasesSlotWhenHandlerReturns(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	h := LimitConcurrency(limiter, time.Second, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	provided := ProvideInitLimiter(limiter, time.Second, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	provided := ProvideInitLimiter(limiter, time.Second, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

type fakePollShedRecorder struct {
	mu    sync.Mutex
	sheds []string
}

func (f *fakePollShedRecorder) RecordPollShed(envName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sheds = append(f.sheds, envName)
}

func TestAcquireInitSlotRecordsAPollShed(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	hold, ok := limiter.Acquire(context.Background())
	require.True(t, ok)
	defer hold()

	rec := &fakePollShedRecorder{}
	h := LimitConcurrency(limiter, time.Second, rec)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the shed request must not reach the handler")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Len(t, rec.sheds, 1, "a poll shed must be recorded")
	// With no environment in the context, the name is empty rather than a panic; the routes
	// that carry an environment attach its display name.
	require.Equal(t, "", rec.sheds[0])

	// An admitted request records nothing.
	hold()
	served := false
	h2 := LimitConcurrency(limiter, time.Second, rec)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }))
	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	require.True(t, served)
	require.Len(t, rec.sheds, 1)

	// A client that left the queue is not budget pressure, so it is not a shed. The
	// rejected counter reports it under the client_gone cause.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	wGone := httptest.NewRecorder()
	h.ServeHTTP(wGone, httptest.NewRequest("GET", "/", nil).WithContext(cancelled))
	require.Equal(t, http.StatusServiceUnavailable, wGone.Code)
	require.Len(t, rec.sheds, 1, "a client that left must not count as a shed")

	// A shutdown drain is not budget pressure either.
	limiter.Close()
	wShutdown := httptest.NewRecorder()
	h.ServeHTTP(wShutdown, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, wShutdown.Code)
	require.Len(t, rec.sheds, 1, "a shutdown rejection must not count as a shed")
}
