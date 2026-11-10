package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/middleware"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"go.opentelemetry.io/otel/codes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latestAllBody is the FDv1 poll payload, with each item left as raw JSON.
type latestAllBody struct {
	Flags    map[string]json.RawMessage `json:"flags"`
	Segments map[string]json.RawMessage `json:"segments"`
}

func latestAllRequest() *http.Request {
	return st.BuildRequestWithAuth("GET", "/sdk/latest-all", st.EnvMain.Config.SDKKey, nil)
}

func TestFDv1PollingEndpoint(t *testing.T) {
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("serves every flag and segment", func(t *testing.T) {
			result, body := st.DoRequest(latestAllRequest(), p.relay)
			require.Equal(t, http.StatusOK, result.StatusCode)
			st.AssertNonStreamingHeaders(t, result.Header)
			assert.Equal(t, "application/json", result.Header.Get("Content-Type"))

			var payload latestAllBody
			require.NoError(t, json.Unmarshal(body, &payload))

			require.Len(t, payload.Flags, len(st.AllFlags))
			for _, f := range st.AllFlags {
				raw, ok := payload.Flags[f.Flag.Key]
				require.True(t, ok, "flag %q is missing", f.Flag.Key)
				var got ldmodel.FeatureFlag
				require.NoError(t, json.Unmarshal(raw, &got))
				assert.Equal(t, f.Flag.Version, got.Version)
			}

			require.Len(t, payload.Segments, 1)
			var segment ldmodel.Segment
			require.NoError(t, json.Unmarshal(payload.Segments[st.Segment1.Key], &segment))
			assert.Equal(t, st.Segment1.Key, segment.Key)
		})

		t.Run("an SDK with the current Etag gets a 304", func(t *testing.T) {
			result, _ := st.DoRequest(latestAllRequest(), p.relay)
			require.Equal(t, http.StatusOK, result.StatusCode)
			etag := result.Header.Get("Etag")
			require.NotEmpty(t, etag)

			r := latestAllRequest()
			r.Header.Set("If-None-Match", etag)
			result, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusNotModified, result.StatusCode)
			assert.Equal(t, etag, result.Header.Get("Etag"), "a 304 reply must repeat the Etag")
			assert.Empty(t, body)

			r = latestAllRequest()
			r.Header.Set("If-None-Match", `W/"stale"`)
			result, body = st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			assert.Equal(t, etag, result.Header.Get("Etag"))
			assert.NotEmpty(t, body)
		})

		t.Run("a filter parameter is ignored", func(t *testing.T) {
			_, unfiltered := st.DoRequest(latestAllRequest(), p.relay)
			r := st.BuildRequestWithAuth("GET", "/sdk/latest-all?filter=microservice-a", st.EnvMain.Config.SDKKey, nil)
			result, filtered := st.DoRequest(r, p.relay)
			require.Equal(t, http.StatusOK, result.StatusCode)
			assert.JSONEq(t, string(unfiltered), string(filtered))
		})

		t.Run("an unknown SDK key is refused", func(t *testing.T) {
			r := st.BuildRequestWithAuth("GET", "/sdk/latest-all", st.UndefinedSDKKey, nil)
			result, _ := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})
	})
}

func TestFDv1PollRepliesUnavailableBeforeTheStoreHasData(t *testing.T) {
	// A zero-value FakeStore has no selector, so it reports that it is not initialized.
	req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, envWithStore(&testclient.FakeStore{}))
	w := httptest.NewRecorder()

	pollLatestAllHandler(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "Service not initialized")
}

func TestFDv1PollLeavesOutDeletedItems(t *testing.T) {
	recorder := installSpanRecorder(t)

	segment := ldbuilders.NewSegmentBuilder("live-segment").Version(3).Build()
	store := testclient.NewFakeStore([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{
			liveFlag("live-flag"),
			tombstone("deleted-flag", 7),
		}},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: segment.Key, Item: ldstoretypes.ItemDescriptor{Version: segment.Version, Item: &segment}},
			tombstone("deleted-segment", 4),
		}},
	})
	req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, envWithStore(store))
	w := httptest.NewRecorder()

	pollLatestAllHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var payload latestAllBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Len(t, payload.Flags, 1)
	assert.Contains(t, payload.Flags, "live-flag")
	assert.Len(t, payload.Segments, 1)
	assert.Contains(t, payload.Segments, "live-segment")

	serialize := requireSpan(t, recorder.Ended(), tracing.SpanSerializePayload)
	attrs := spanAttrs(serialize)
	assert.Equal(t, int64(1), attrs[tracing.FlagCountKey].AsInt64())
	assert.Equal(t, int64(1), attrs[tracing.SegmentCountKey].AsInt64())
	assert.Equal(t, int64(w.Body.Len()), attrs[tracing.PayloadSizeKey].AsInt64())
}

func TestFDv1PollEtagTracksTheData(t *testing.T) {
	flagAt := func(key string, version int) ldstoretypes.KeyedItemDescriptor {
		flag := ldbuilders.NewFlagBuilder(key).Version(version).SingleVariation(ldvalue.Bool(true)).Build()
		return ldstoretypes.KeyedItemDescriptor{Key: key, Item: ldstoretypes.ItemDescriptor{Version: version, Item: &flag}}
	}
	segmentAt := func(key string, version int) ldstoretypes.KeyedItemDescriptor {
		segment := ldbuilders.NewSegmentBuilder(key).Version(version).Build()
		return ldstoretypes.KeyedItemDescriptor{Key: key, Item: ldstoretypes.ItemDescriptor{Version: version, Item: &segment}}
	}
	items := func(descriptors ...ldstoretypes.KeyedItemDescriptor) []ldstoretypes.KeyedItemDescriptor {
		return descriptors
	}

	base := latestAllEtag(items(flagAt("a", 1), flagAt("b", 2)), items(segmentAt("s", 1)))

	t.Run("the order of the items does not change it", func(t *testing.T) {
		assert.Equal(t, base, latestAllEtag(items(flagAt("b", 2), flagAt("a", 1)), items(segmentAt("s", 1))))
	})

	t.Run("a new flag version changes it", func(t *testing.T) {
		assert.NotEqual(t, base, latestAllEtag(items(flagAt("a", 2), flagAt("b", 2)), items(segmentAt("s", 1))))
	})

	t.Run("a new segment version changes it", func(t *testing.T) {
		assert.NotEqual(t, base, latestAllEtag(items(flagAt("a", 1), flagAt("b", 2)), items(segmentAt("s", 2))))
	})

	t.Run("a deleted flag changes it", func(t *testing.T) {
		assert.NotEqual(t, base, latestAllEtag(items(flagAt("a", 1)), items(segmentAt("s", 1))))
	})

	t.Run("a flag and a segment with the same key and version are distinct", func(t *testing.T) {
		assert.NotEqual(t,
			latestAllEtag(items(flagAt("x", 1)), nil),
			latestAllEtag(nil, items(segmentAt("x", 1))))
	})
}

// TestSerializeErrorPathsInFDv1Poll reaches each branch that gives up on the payload. Each
// must report the failure on the serialize span, send exactly one 500, and start no
// response-write span.
func TestSerializeErrorPathsInFDv1Poll(t *testing.T) {
	for _, params := range []struct {
		name        string
		collections []ldstoretypes.Collection
		wantStatus  string
	}{
		{
			name: "item under the flag kind is not a flag",
			collections: []ldstoretypes.Collection{
				{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{wrongTypedItem("bad")}},
			},
			wantStatus: "error casting keyed item to feature flag",
		},
		{
			name: "item under the segment kind is not a segment",
			collections: []ldstoretypes.Collection{
				{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{wrongTypedItem("bad")}},
			},
			wantStatus: "error casting keyed item to feature segment",
		},
	} {
		t.Run(params.name, func(t *testing.T) {
			recorder := installSpanRecorder(t)

			store := testclient.NewFakeStore(params.collections)
			req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, envWithStore(store))
			w := &headerCountingResponseWriter{ResponseRecorder: httptest.NewRecorder()}

			pollLatestAllHandler(w, req)

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.Equal(t, 1, w.headerWrites, "the handler should send exactly one status")
			assert.Zero(t, w.Body.Len(), "a failed serialization should send no payload")

			spans := recorder.Ended()
			serialize := requireSpan(t, spans, tracing.SpanSerializePayload)
			assert.Equal(t, codes.Error, serialize.Status().Code)
			assert.Equal(t, params.wantStatus, serialize.Status().Description)
			assertRecordedError(t, serialize, params.wantStatus)

			assert.Empty(t, spansNamed(spans, tracing.SpanWriteResponse),
				"no response was written, so there should be no write span")
			assert.Zero(t, countStarted(recorder.Started(), tracing.SpanWriteResponse))
		})
	}
}

// TestFDv1PollRequestsShareOneEtagRead blocks one request inside its store read, then sends
// more requests with the same Etag. They join the first flight, so the store is read one time
// and every request gets a 304 reply.
func TestFDv1PollRequestsShareOneEtagRead(t *testing.T) {
	store := storeWithOneFlag()
	env := envWithStore(store)
	etag := doTracedPollRequest(env, pollLatestAllHandler, "").Header().Get("Etag")
	require.NotEmpty(t, etag)

	entered := make(chan struct{})
	release := make(chan struct{})
	reads := blockFirstRead(func(hook func()) { store.SnapshotHook = hook }, entered, release)

	conditionalRequest := func() *httptest.ResponseRecorder {
		req := buildPreRoutedRequest("GET", nil, http.Header{"If-None-Match": {etag}}, nil, env)
		w := httptest.NewRecorder()
		pollLatestAllHandler(w, req)
		return w
	}

	const followers = 3
	responses := make(chan *httptest.ResponseRecorder, followers+1)
	go func() { responses <- conditionalRequest() }()
	<-entered
	for range followers {
		go func() { responses <- conditionalRequest() }()
	}
	// The followers need only a short CPU path to reach the flight group. A follower that
	// arrives after the release reads the store itself, and the read count below fails.
	time.Sleep(100 * time.Millisecond)
	close(release)

	for range followers + 1 {
		w := <-responses
		assert.Equal(t, http.StatusNotModified, w.Code)
		assert.Equal(t, etag, w.Header().Get("Etag"))
	}
	assert.Equal(t, int32(1), reads.Load(), "the store should be read one time, not one time for each request")
}

// TestFDv1PollReadsTheDataAfterItWaitsForASlot queues a request behind a full budget, changes
// the data, and then frees the slot. The reply must contain the data from after the change.
func TestFDv1PollReadsTheDataAfterItWaitsForASlot(t *testing.T) {
	store := storeWithOneFlag()
	env := envWithStore(store)

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 1})
	hold, ok := limiter.Acquire(context.Background())
	require.True(t, ok)
	handler := middleware.ProvideInitLimiter(limiter, time.Minute, nil)(http.HandlerFunc(pollLatestAllHandler))

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, env)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		done <- w
	}()
	require.Eventually(t, func() bool { return limiter.Stats().Waiting == 1 }, time.Second, time.Millisecond)

	newFlag := ldbuilders.NewFlagBuilder("flag-two").Version(1).SingleVariation(ldvalue.Bool(true)).Build()
	newFlagJSON, err := json.Marshal(newFlag)
	require.NoError(t, err)
	builder := subsystems.NewChangeSetBuilder()
	builder.Start(subsystems.ServerIntent{Payload: subsystems.Payload{
		ID: "next-state", Target: 2, Code: subsystems.IntentTransferFull, Reason: "payload-missing",
	}})
	builder.AddPut(subsystems.FlagKind, newFlag.Key, newFlag.Version, newFlagJSON)
	changeSet, err := builder.Finish(subsystems.NewSelector("next-state", 2))
	require.NoError(t, err)
	store.Apply(*changeSet)

	hold()
	w := <-done

	require.Equal(t, http.StatusOK, w.Code)
	var payload latestAllBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Contains(t, payload.Flags, "flag-two")
	assert.NotContains(t, payload.Flags, "flag-one")
	newFlagDescriptor := ldstoretypes.KeyedItemDescriptor{Key: newFlag.Key, Item: ldstoretypes.ItemDescriptor{Version: 1, Item: &newFlag}}
	assert.Equal(t, formatEtag(latestAllEtag([]ldstoretypes.KeyedItemDescriptor{newFlagDescriptor}, nil)), w.Header().Get("Etag"))
}

// TestEnvironmentTTLAppliesOnlyToPHPEndpoints checks that the environment TTL adds an Expires
// header to the PHP polls only. The HTTP cache of the Go and Java SDKs honors Expires, so an
// Expires header on their polls stops them from seeing updates until it expires.
func TestEnvironmentTTLAppliesOnlyToPHPEndpoints(t *testing.T) {
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvWithTTL)
	sdkKey := st.EnvWithTTL.Config.SDKKey

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, tc := range []struct {
			path        string
			wantExpires bool
		}{
			{"/sdk/flags", true},
			{"/sdk/flags/" + st.Flag1ServerSide.Flag.Key, true},
			{"/sdk/segments/" + st.Segment1.Key, true},
			{"/sdk/latest-all", false},
			{"/sdk/poll", false},
		} {
			t.Run(tc.path, func(t *testing.T) {
				result, _ := st.DoRequest(st.BuildRequestWithAuth("GET", tc.path, sdkKey, nil), p.relay)
				require.Equal(t, http.StatusOK, result.StatusCode)
				if tc.wantExpires {
					assert.NotEmpty(t, result.Header.Get("Expires"))
				} else {
					assert.Empty(t, result.Header.Get("Expires"))
				}
			})
		}
	})
}
