package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"

	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/middleware"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest/testenv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFDv2ClientSidePollingWithMobileKey(t *testing.T) {
	env := st.EnvWithAllCredentials
	mobileKey := env.Config.MobileKey
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("GET with context in URL", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))

			// Should have server-intent + put-objects for mobile-visible flags + payload-transferred
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
			assert.Equal(t, "payload-transferred", payload.Events[len(payload.Events)-1].Event)

			// Verify put-objects use flag-eval kind
			for _, event := range payload.Events {
				if event.Event == "put-object" {
					var putObj subsystems.PutObject
					data, err := json.Marshal(event.EventData)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(data, &putObj))
					assert.Equal(t, subsystems.ObjectKind("flag-eval"), putObj.Kind)
				}
			}
		})

		t.Run("POST with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(mobileKey))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("POST", "/sdk/poll/eval", contextJSON, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
		})

		t.Run("auth via query param", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64+"?auth="+string(mobileKey), nil, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("invalid credential returns 401", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, st.UndefinedMobileKey, nil)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})

		t.Run("no credential returns 401", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, nil)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})

		t.Run("basis param returns up-to-date when matching", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64+"?basis=initial-state", mobileKey, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.Len(t, payload.Events, 1)
			assert.Equal(t, "server-intent", payload.Events[0].Event)
		})

		t.Run("ETag caching", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			etag := result.Header.Get("Etag")
			assert.NotEmpty(t, etag)

			req = st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			req.Header.Set("If-None-Match", etag)
			result, _ = st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusNotModified, result.StatusCode)
			assert.Equal(t, etag, result.Header.Get("Etag"), "a 304 reply must repeat the Etag")

			req = st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			req.Header.Set("If-None-Match", "wrong-etag")
			result, _ = st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			assert.NotEmpty(t, result.Header.Get("Etag"))
		})
	})
}

func TestFDv2ClientSidePollingWithEnvironmentID(t *testing.T) {
	env := st.EnvWithAllCredentials
	envID := env.Config.EnvID
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("GET with context in URL via header", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
			assert.Equal(t, "server-intent", payload.Events[0].Event)

			// Verify put-objects use flag-eval kind
			for _, event := range payload.Events {
				if event.Event == "put-object" {
					var putObj subsystems.PutObject
					data, err := json.Marshal(event.EventData)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(data, &putObj))
					assert.Equal(t, subsystems.ObjectKind("flag-eval"), putObj.Kind)
				}
			}
		})

		t.Run("POST with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("POST", "/sdk/poll/eval", contextJSON, h)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("auth via query param", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64+"?auth="+string(envID), nil, nil)
			result, body := st.DoRequest(req, p.relay)

			assert.Equal(t, http.StatusOK, result.StatusCode)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))
			assert.NotEmpty(t, payload.Events)
		})

		t.Run("invalid credential returns 401", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(st.UndefinedEnvID))
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, h)
			result, _ := st.DoRequest(req, p.relay)
			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})
	})
}

func TestFDv2ClientSidePollingFiltersFlagsByCredentialType(t *testing.T) {
	env := st.EnvWithAllCredentials
	mobileKey := env.Config.MobileKey
	envID := env.Config.EnvID
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("mobile key sees mobile-visible flags", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
			_, body := st.DoRequest(req, p.relay)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))

			flagKeys := collectFlagKeys(t, payload)
			// MobileFlags includes: Flag1ServerSide, Flag2ServerSide, Flag4ClientSide, Flag5ClientSide, Flag7Mobile, Flag8ContextAware
			// Flag3ServerSideNotMobile and Flag6ClientSideNotMobile should be excluded
			assert.NotContains(t, flagKeys, st.Flag3ServerSideNotMobile.Flag.Key)
			assert.NotContains(t, flagKeys, st.Flag6ClientSideNotMobile.Flag.Key)
			assert.Contains(t, flagKeys, st.Flag7Mobile.Flag.Key)
		})

		t.Run("environment ID sees client-side-visible flags", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			req := st.BuildRequest("GET", "/sdk/poll/eval/"+contextBase64, nil, h)
			_, body := st.DoRequest(req, p.relay)

			var payload pollingPayload
			require.NoError(t, json.Unmarshal(body, &payload))

			flagKeys := collectFlagKeys(t, payload)
			// ClientSideFlags includes: Flag4ClientSide, Flag5ClientSide, Flag6ClientSideNotMobile, Flag8ContextAware
			// Server-side only and mobile-only flags should be excluded
			assert.NotContains(t, flagKeys, st.Flag1ServerSide.Flag.Key)
			assert.NotContains(t, flagKeys, st.Flag2ServerSide.Flag.Key)
			assert.NotContains(t, flagKeys, st.Flag7Mobile.Flag.Key)
			assert.Contains(t, flagKeys, st.Flag4ClientSide.Flag.Key)
			assert.Contains(t, flagKeys, st.Flag6ClientSideNotMobile.Flag.Key)
		})
	})
}

func collectFlagKeys(t *testing.T, payload pollingPayload) []string {
	t.Helper()
	var keys []string
	for _, event := range payload.Events {
		if event.Event == "put-object" {
			data, err := json.Marshal(event.EventData)
			require.NoError(t, err)
			var putObj subsystems.PutObject
			require.NoError(t, json.Unmarshal(data, &putObj))
			keys = append(keys, putObj.Key)
		}
	}
	return keys
}

func TestFDv2ClientSidePollingWithoutSelector(t *testing.T) {
	flag := ldbuilders.NewFlagBuilder("flag-a").Version(1).SingleVariation(ldvalue.Bool(true)).
		ClientSideUsingEnvironmentID(true).Build()
	store := testclient.NewFakeStoreWithoutSelector([]ldstoretypes.Collection{
		{Kind: ldstoreimpl.Features(), Items: []ldstoretypes.KeyedItemDescriptor{
			{Key: flag.Key, Item: ldstoretypes.ItemDescriptor{Version: flag.Version, Item: &flag}},
		}},
		{Kind: ldstoreimpl.Segments(), Items: []ldstoretypes.KeyedItemDescriptor{}},
	})
	// The client is not initialized, as when its initialization timed out and Relay serves the
	// data that the persistent store already holds.
	env := testenv.NewTestEnvContextWithClientFactory("",
		testclient.FakeLDClientFactoryWithStore(false, store), nil)
	limiter := concurrency.New("init", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	handler := middleware.ProvideInitLimiter(limiter, time.Second, nil)(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			pollEvalHandlerV2Shared(w, req, ct.OptBase2Bytes{})
		}))
	poll := func(query string, ifNoneMatch string) *httptest.ResponseRecorder {
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		if ifNoneMatch != "" {
			headers.Set("If-None-Match", ifNoneMatch)
		}
		req := buildPreRoutedRequest("REPORT", []byte(`{"kind":"user","key":"me"}`), headers, nil, env)
		req.URL.RawQuery = query
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	for _, query := range []string{"", "basis=some-state"} {
		t.Run("serves a full payload for query "+strconv.Quote(query), func(t *testing.T) {
			w := poll(query, "")
			require.Equal(t, http.StatusOK, w.Code)
			assertFullPayloadWithoutState(t, w.Body.Bytes(), 1)
			assert.NotEmpty(t, w.Header().Get("Etag"))
			assert.NotEqual(t, `W/""`, w.Header().Get("Etag"))
		})
	}

	t.Run("an SDK with the current Etag gets a 304", func(t *testing.T) {
		etag := poll("", "").Header().Get("Etag")
		assert.Equal(t, http.StatusNotModified, poll("", etag).Code)
		assert.Equal(t, http.StatusOK, poll("", `W/""`).Code)
	})

	// The 304 must not need a slot when the Etag is a hash of the data rather than a selector.
	t.Run("an SDK with the current Etag gets a 304 while the budget is full", func(t *testing.T) {
		etag := poll("", "").Header().Get("Etag")

		hold, ok := limiter.Acquire(context.Background())
		require.True(t, ok)
		defer hold()

		assert.Equal(t, http.StatusNotModified, poll("", etag).Code)
		assert.NotEqual(t, http.StatusNotModified, poll("", `W/""`).Code)
	})
}
