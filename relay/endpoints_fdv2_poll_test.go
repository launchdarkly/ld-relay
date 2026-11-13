package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fdv2PollEndpointTestParams struct {
	endpointTestParams
	expectedEvents    []string
	expectedEventData [][]byte
}

func TestFDv2PollingEndpoint(t *testing.T) {
	sdkKeyMain := st.EnvMain.Config.SDKKey

	specs := []fdv2PollEndpointTestParams{
		{
			endpointTestParams: endpointTestParams{"poll", "GET", "/sdk/poll", nil, sdkKeyMain, http.StatusOK, st.ExpectNoBody()},
			expectedEvents: []string{
				"server-intent",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"payload-transferred",
			},
		},
		{
			endpointTestParams: endpointTestParams{"poll", "GET", "/sdk/poll?basis=initial-state", nil, sdkKeyMain, http.StatusOK, st.ExpectNoBody()},
			expectedEvents: []string{
				"server-intent",
			},
		},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvWithTTL)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			t.Run(spec.name, func(t *testing.T) {
				result, body := st.DoRequest(spec.request(), p.relay)

				var payload pollingPayload
				err := json.Unmarshal(body, &payload)
				assert.NoError(t, err)

				assert.Len(t, payload.Events, len(spec.expectedEvents), "Unexpected number of events")

				assert.Equal(t, spec.expectedStatus, result.StatusCode)
				st.AssertNonStreamingHeaders(t, result.Header)
			})
		}

		t.Run("ETag caching", func(t *testing.T) {
			spec := specs[0]

			result, _ := st.DoRequest(spec.request(), p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			etag := result.Header.Get("Etag")
			assert.NotEmpty(t, etag)

			r := spec.request()
			r.Header.Set("If-None-Match", etag)
			result, _ = st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusNotModified, result.StatusCode)
			assert.Equal(t, etag, result.Header.Get("Etag"), "a 304 reply must repeat the Etag")

			r = spec.request()
			r.Header.Set("If-None-Match", "wrong-etag")
			result, _ = st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			assert.NotEmpty(t, result.Header.Get("Etag"))
		})
	})
}

// fdv2PollEvents is an FDv2 polling payload with the data of each event left as raw JSON.
type fdv2PollEvents struct {
	Events []struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	} `json:"events"`
}

// assertFullPayloadWithoutState checks that body is a full transfer whose server intent and
// payload-transferred event carry no state, which is what an SDK reads as having no selector.
func assertFullPayloadWithoutState(t *testing.T, body []byte, putObjects int) {
	t.Helper()
	var payload fdv2PollEvents
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Len(t, payload.Events, putObjects+2)

	first, last := payload.Events[0], payload.Events[len(payload.Events)-1]
	require.Equal(t, "server-intent", first.Event)
	var intent subsystems.ServerIntent
	require.NoError(t, json.Unmarshal(first.Data, &intent))
	assert.Equal(t, subsystems.IntentTransferFull, intent.Payload.Code)
	assert.Empty(t, intent.Payload.ID)

	require.Equal(t, "payload-transferred", last.Event)
	assert.JSONEq(t, `{"state":"","version":0}`, string(last.Data))
}

func TestFDv2PollWithoutSelector(t *testing.T) {
	store := testclient.NewFakeStoreWithoutSelector([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{liveFlag("flag-a"), liveFlag("flag-b")}},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{}},
	})
	env := envWithStore(store)
	poll := func(query string, headers http.Header) *httptest.ResponseRecorder {
		if headers == nil {
			headers = make(http.Header)
		}
		req := buildPreRoutedRequest("GET", nil, headers, nil, env)
		req.URL.RawQuery = query
		w := httptest.NewRecorder()
		pollHandlerV2(w, req)
		return w
	}

	for _, query := range []string{"", "basis=some-state"} {
		t.Run("serves a full payload for query "+strconv.Quote(query), func(t *testing.T) {
			w := poll(query, nil)
			require.Equal(t, http.StatusOK, w.Code)
			assertFullPayloadWithoutState(t, w.Body.Bytes(), 2)
			assert.NotEmpty(t, w.Header().Get("Etag"))
			assert.NotEqual(t, `W/""`, w.Header().Get("Etag"))
		})
	}

	t.Run("the Etag identifies the data", func(t *testing.T) {
		etag := poll("", nil).Header().Get("Etag")

		h := make(http.Header)
		h.Set("If-None-Match", etag)
		assert.Equal(t, http.StatusNotModified, poll("", h).Code)

		h.Set("If-None-Match", `W/""`)
		assert.Equal(t, http.StatusOK, poll("", h).Code)

		other := envWithStore(testclient.NewFakeStoreWithoutSelector([]ldstoretypes.Collection{
			{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{liveFlag("flag-a")}},
		}))
		w := httptest.NewRecorder()
		pollHandlerV2(w, buildPreRoutedRequest("GET", nil, make(http.Header), nil, other))
		require.Equal(t, http.StatusOK, w.Code)
		assert.NotEqual(t, etag, w.Header().Get("Etag"))
	})
}

func TestFDv2PollRepliesUnavailableBeforeTheStoreHasData(t *testing.T) {
	// A zero-value FakeStore has no selector, so it reports that it is not initialized.
	req := buildPreRoutedRequest("GET", nil, make(http.Header), nil, envWithStore(&testclient.FakeStore{}))
	w := httptest.NewRecorder()

	pollHandlerV2(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "Service not initialized")
}
