package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"
	"github.com/launchdarkly/ld-relay/v9/internal/tracing"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerCountingResponseWriter counts WriteHeader calls. net/http and httptest both swallow a
// second WriteHeader (logging "superfluous response.WriteHeader call" at most), so counting is
// the only way a test can see that a handler wrote a status twice -- which is exactly what
// dropping one of the `if !ok { return }` guards after a failed serialization would do.
type headerCountingResponseWriter struct {
	*httptest.ResponseRecorder
	headerWrites int
}

func (w *headerCountingResponseWriter) WriteHeader(code int) {
	w.headerWrites++
	w.ResponseRecorder.WriteHeader(code)
}

// unknownDataKind is a data kind Relay does not recognize, for the default branch of the
// snapshot loop.
type unknownDataKind struct{}

func (unknownDataKind) GetName() string { return "unknown-kind" }

func (unknownDataKind) Serialize(ldstoretypes.ItemDescriptor) []byte { return nil }

func (unknownDataKind) Deserialize([]byte) (ldstoretypes.ItemDescriptor, error) {
	return ldstoretypes.ItemDescriptor{}, nil
}

// envWithStore builds an environment context whose read-only store is the given fake store, so a
// test can serve data the standard fixtures cannot express.
func envWithStore(store *testclient.FakeStore) relayenv.EnvContext {
	return testenv.NewTestEnvContextWithClientFactory("",
		testclient.FakeLDClientFactoryWithStore(true, store), nil)
}

func liveFlag(key string) ldstoretypes.KeyedItemDescriptor {
	flag := ldbuilders.NewFlagBuilder(key).Version(1).SingleVariation(ldvalue.String("v")).Build()
	return ldstoretypes.KeyedItemDescriptor{
		Key:  key,
		Item: ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag},
	}
}

// tombstone is the placeholder GetAll is contractually required to return for a deleted item.
func tombstone(key string, version int) ldstoretypes.KeyedItemDescriptor {
	return ldstoretypes.KeyedItemDescriptor{
		Key:  key,
		Item: ldstoretypes.ItemDescriptor{Version: version, Item: nil},
	}
}

// wrongTypedItem is an item stored under a kind whose Go type does not match it.
func wrongTypedItem(key string) ldstoretypes.KeyedItemDescriptor {
	return ldstoretypes.KeyedItemDescriptor{
		Key:  key,
		Item: ldstoretypes.ItemDescriptor{Version: 1, Item: "not a flag or a segment"},
	}
}

// TestFlagCountExcludesTombstones covers the /sdk/flags count. GetAll includes placeholders for
// deleted flags and serializeFlagsAsMap skips them, so counting the raw result reported flags
// that were never in the response.
func TestFlagCountExcludesTombstones(t *testing.T) {
	recorder := installSpanRecorder(t)

	store := testclient.NewFakeStore([]ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				liveFlag("live-flag"),
				tombstone("deleted-one", 7),
				tombstone("deleted-two", 9),
			},
		},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{}},
	})

	req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, envWithStore(store))
	w := httptest.NewRecorder()

	pollAllFlagsHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body, 1, "only the live flag should be serialized")

	serialize := requireSpan(t, recorder.Ended(), tracing.SpanSerializePayload)
	assert.Equal(t, int64(len(body)), spanAttrs(serialize)[tracing.FlagCountKey].AsInt64(),
		"the flag count should match the flags in the response, not the raw store entries")
}

// TestSerializeErrorPathsInPollHandlerV2 reaches the three branches of the snapshot loop that
// give up on the payload. Each must report the failure on the serialize span, send exactly one
// 500, and start no response-write span -- the last two being what the `if !ok { return }` guard
// after the closure is for.
func TestSerializeErrorPathsInPollHandlerV2(t *testing.T) {
	segment := ldbuilders.NewSegmentBuilder("a-segment").Version(1).Build()

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
		{
			name: "a data kind Relay does not recognize",
			collections: []ldstoretypes.Collection{
				{Kind: unknownDataKind{}, Items: []ldstoretypes.KeyedItemDescriptor{
					{Key: "x", Item: ldstoretypes.ItemDescriptor{Version: 1, Item: &segment}},
				}},
			},
			wantStatus: "unexpected data kind in store snapshot",
		},
	} {
		t.Run(params.name, func(t *testing.T) {
			recorder := installSpanRecorder(t)

			store := testclient.NewFakeStore(params.collections)
			req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, envWithStore(store))
			w := &headerCountingResponseWriter{ResponseRecorder: httptest.NewRecorder()}

			pollHandlerV2(w, req)

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

// TestSerializeErrorPathInPollFlagOrSegment reaches the json.Marshal failure branch by storing an
// item that cannot be marshaled, and makes the same assertions.
func TestSerializeErrorPathInPollFlagOrSegment(t *testing.T) {
	recorder := installSpanRecorder(t)

	// A channel has no JSON representation, so json.Marshal fails on the stored item.
	unmarshalable := ldstoretypes.KeyedItemDescriptor{
		Key:  "bad-flag",
		Item: ldstoretypes.ItemDescriptor{Version: 1, Item: make(chan int)},
	}
	store := testclient.NewFakeStore([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{unmarshalable}},
	})

	env := envWithStore(store)
	req := buildPreRoutedRequest("GET", nil, make(http.Header), map[string]string{"key": "bad-flag"}, env)
	w := &headerCountingResponseWriter{ResponseRecorder: httptest.NewRecorder()}

	pollFlagOrSegment(env, ldstoreimpl.Features())(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, 1, w.headerWrites, "the handler should send exactly one status")
	assert.Zero(t, w.Body.Len())

	spans := recorder.Ended()
	serialize := requireSpan(t, spans, tracing.SpanSerializePayload)
	assert.Equal(t, codes.Error, serialize.Status().Code)
	assertRecordedError(t, serialize, "json")

	assert.Empty(t, spansNamed(spans, tracing.SpanWriteResponse))
	assert.Zero(t, countStarted(recorder.Started(), tracing.SpanWriteResponse))
}

// assertRecordedError checks that RecordError was called, not just SetStatus: the error has to
// reach the span as an exception event to show up in a trace viewer.
func assertRecordedError(t *testing.T, span sdktrace.ReadOnlySpan, containing string) {
	t.Helper()
	for _, event := range span.Events() {
		if event.Name != "exception" {
			continue
		}
		for _, kv := range event.Attributes {
			if kv.Key == "exception.message" {
				assert.Contains(t, kv.Value.AsString(), containing)
				return
			}
		}
	}
	assert.Failf(t, "no recorded error", "span %q has no exception event; events: %v", span.Name(), span.Events())
}
