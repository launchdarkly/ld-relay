package relay

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doTracedPollRequest runs one pre-routed request against handler on the calling goroutine,
// wrapped in its own root span the way otelmux wraps a routed request, and returns the response.
func doTracedPollRequest(env relayenv.EnvContext, handler http.HandlerFunc, rawQuery string) *httptest.ResponseRecorder {
	req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, env)
	req.URL.RawQuery = rawQuery
	ctx, span := tracing.Tracer().Start(req.Context(), "test.request")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler(w, req)
	span.End()
	return w
}

func storeWithOneFlag() *testclient.FakeStore {
	return testclient.NewFakeStore([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{liveFlag("flag-one")}},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{}},
	})
}

// blockFirstRead installs hook-driven gating on a store read: the first read signals `entered` and
// then blocks until `release` is closed; later reads pass straight through. The returned counter
// reports how many reads happened. It is installed after the environment is built so that only the
// handlers' reads are observed.
func blockFirstRead(setHook func(func()), entered chan<- struct{}, release <-chan struct{}) *atomic.Int32 {
	var reads atomic.Int32
	setHook(func() {
		if reads.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	return &reads
}

// TestPollingHandlersShareOnePayloadBuildAcrossConcurrentRequests blocks one request inside the
// store read and then issues more identical requests. Because the first request cannot leave the
// flight group until the test releases it, the followers join its flight: the store is read and
// the payload serialized exactly once, and every request receives the same response.
func TestPollingHandlersShareOnePayloadBuildAcrossConcurrentRequests(t *testing.T) {
	cases := []struct {
		name          string
		handler       http.HandlerFunc
		setReadHook   func(*testclient.FakeStore, func())
		storeSpanName string
	}{
		{
			name:          "pollHandlerV2 GET /sdk/poll",
			handler:       pollHandlerV2,
			setReadHook:   func(s *testclient.FakeStore, hook func()) { s.SnapshotHook = hook },
			storeSpanName: tracing.SpanStoreSnapshot,
		},
		{
			name:          "pollAllFlagsHandler GET /sdk/flags",
			handler:       pollAllFlagsHandler,
			setReadHook:   func(s *testclient.FakeStore, hook func()) { s.GetAllHook = hook },
			storeSpanName: tracing.SpanStoreGetAll,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := installSpanRecorder(t)

			store := storeWithOneFlag()
			env := envWithStore(store)

			entered := make(chan struct{})
			release := make(chan struct{})
			reads := blockFirstRead(func(hook func()) { tc.setReadHook(store, hook) }, entered, release)

			const followers = 3
			responses := make(chan *httptest.ResponseRecorder, followers+1)

			// The leader enters the store read and blocks there, holding its flight open.
			go func() { responses <- doTracedPollRequest(env, tc.handler, "") }()
			<-entered

			// The followers reach the flight group while the leader's flight is still open, so
			// they join it instead of reading the store themselves. The sleep is generous time
			// for them to get from goroutine start to the flight-group call (a short pure-CPU
			// path); a follower that somehow had not arrived before the release would read the
			// store itself and loudly fail the read-count assertion below.
			for range followers {
				go func() { responses <- doTracedPollRequest(env, tc.handler, "") }()
			}
			time.Sleep(100 * time.Millisecond)
			close(release)

			var bodies, etags []string
			for range followers + 1 {
				w := <-responses
				require.Equal(t, http.StatusOK, w.Code)
				bodies = append(bodies, w.Body.String())
				etags = append(etags, w.Header().Get("Etag"))
			}
			for i := 1; i <= followers; i++ {
				assert.Equal(t, bodies[0], bodies[i], "every request should receive the same payload")
				assert.Equal(t, etags[0], etags[i], "every request should receive the same Etag")
			}
			assert.Equal(t, int32(1), reads.Load(), "the store should be read once, not once per request")

			spans := recorder.Ended()
			assert.Len(t, spansNamed(spans, tc.storeSpanName), 1)
			serialize := requireSpan(t, spans, tracing.SpanSerializePayload)
			assert.Len(t, spansNamed(spans, tracing.SpanWriteResponse), followers+1,
				"every request should still write its own response")

			// Every request's own span reports that the payload build was shared. The one
			// request that executed the build -- the one whose trace carries the serialize
			// span -- did not wait and must record no wait time; every follower must record
			// how long it waited.
			roots := spansNamed(spans, "test.request")
			require.Len(t, roots, followers+1)
			executedRoots, waitedRoots := 0, 0
			for _, root := range roots {
				attrs := spanAttrs(root)
				shared, ok := attrs[tracing.SingleflightSharedKey]
				require.True(t, ok, "the request span should report whether the payload build was shared")
				assert.True(t, shared.AsBool())

				wait, waited := attrs[tracing.SingleflightWaitMSKey]
				if root.SpanContext().TraceID() == serialize.SpanContext().TraceID() {
					executedRoots++
					assert.False(t, waited, "the request that built the payload did not wait")
				} else {
					waitedRoots++
					require.True(t, waited, "a request that shared another's payload build should record its wait")
					assert.Positive(t, wait.AsFloat64())
				}
			}
			assert.Equal(t, 1, executedRoots)
			assert.Equal(t, followers, waitedRoots)
		})
	}
}

// TestPollHandlerV2DoesNotShareAcrossDifferentBasisValues proves the basis is part of the flight
// key: while a no-basis request is still blocked inside its flight, a request whose basis matches
// the store's selector state completes on its own with the small "up-to-date" payload -- which is
// only possible if it did not join the blocked flight.
func TestPollHandlerV2DoesNotShareAcrossDifferentBasisValues(t *testing.T) {
	store := storeWithOneFlag()
	env := envWithStore(store)

	entered := make(chan struct{})
	release := make(chan struct{})
	reads := blockFirstRead(func(hook func()) { store.SnapshotHook = hook }, entered, release)

	leaderResp := make(chan *httptest.ResponseRecorder, 1)
	go func() { leaderResp <- doTracedPollRequest(env, pollHandlerV2, "") }()
	<-entered

	// NewFakeStore's selector state is "initial-state", so this request is up to date.
	upToDate := doTracedPollRequest(env, pollHandlerV2, "basis=initial-state")
	require.Equal(t, http.StatusOK, upToDate.Code)
	assert.Equal(t, int32(2), reads.Load(), "a request with a different basis must run a flight of its own")
	assert.Contains(t, upToDate.Body.String(), "up-to-date")
	assert.NotContains(t, upToDate.Body.String(), "put-object")

	close(release)
	leader := <-leaderResp
	require.Equal(t, http.StatusOK, leader.Code)
	assert.Contains(t, leader.Body.String(), "cant-catchup")
	assert.Contains(t, leader.Body.String(), "put-object")
}

// TestPollingSingleflightAttributesForALoneRequest checks the other side of the flight-group
// annotations: a request that shares its flight with nobody reports shared=false, and records no
// wait time because it executed the build itself.
func TestPollingSingleflightAttributesForALoneRequest(t *testing.T) {
	recorder := installSpanRecorder(t)
	env := testenv.MakeTestContextWithData()

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"pollHandlerV2", pollHandlerV2},
		{"pollAllFlagsHandler", pollAllFlagsHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder.Reset()

			w := doTracedPollRequest(env, tc.handler, "")
			require.Equal(t, http.StatusOK, w.Code)

			root := requireSpan(t, recorder.Ended(), "test.request")
			attrs := spanAttrs(root)
			shared, ok := attrs[tracing.SingleflightSharedKey]
			require.True(t, ok, "the request span should always report whether the payload build was shared")
			assert.False(t, shared.AsBool())

			_, waited := attrs[tracing.SingleflightWaitMSKey]
			assert.False(t, waited, "a request that built its own payload should record no wait time")
		})
	}
}
