package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
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
