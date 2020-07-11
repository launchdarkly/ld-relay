package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/eventsource"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/testhelpers"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/logging"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
)

func handler() clientMux {
	return clientMux{clientContextByKey: map[c.SDKCredential]relayenv.EnvContext{
		key(): newTestEnvContext("", true, nil),
	}}
}

func clientSideHandler(allowedOrigins []string) clientSideMux {
	testClientSideContext := &clientSideContext{
		allowedOrigins: allowedOrigins,
		EnvContext:     newTestEnvContext("", true, nil),
	}
	contexts := map[c.SDKCredential]*clientSideContext{key(): testClientSideContext}
	return clientSideMux{contextByKey: contexts}
}

func buildRequest(verb string, vars map[string]string, headers map[string]string, body string, ctx interface{}) *http.Request {
	req, _ := http.NewRequest(verb, "", bytes.NewBufferString(body))
	req = mux.SetURLVars(req, vars)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), contextKey, ctx))
	return req
}

func TestGetFlagEvalValueOnlySucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, makeEvalBody(clientSideFlags, false, false), string(b))
}

func TestReportFlagEvalValueOnlySucceeds(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, makeEvalBody(clientSideFlags, false, false), string(b))
}

func TestGetFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, makeEvalBody(clientSideFlags, true, false), string(b))
}

func TestGetFlagEvalWithReasonsSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	req.URL.RawQuery = "withReasons=true"
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, makeEvalBody(clientSideFlags, true, true), string(b))
}

func TestReportFlagEvalSucceeds(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, makeEvalBody(clientSideFlags, true, false), string(b))
}

func TestAuthorizeMethodFailsOnInvalidAuthKey(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee", "Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}`, nil)
	resp := httptest.NewRecorder()
	handler().selectClientByAuthorizationKey(mobileSdk)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fail() })).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusUnauthorized, resp.Code)
}

func TestFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}notjson`, nil)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestReportFlagEvalFailsWithMissingUserKey(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, "{}", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"User must have a 'key' attribute"}`, string(b))
}

func TestReportFlagEvalFailsallowMethodOptionsHandlerWithUninitializedClientAndStore(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	ctx := newTestEnvContext("", false, makeStoreWithData(false))
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	ctx := newTestEnvContext("", false, makeStoreWithData(true))
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(jsClientSdk)(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)
	assert.JSONEq(t, makeEvalBody(clientSideFlags, false, false), string(b))
}

func TestFindEnvironmentFailsOnInvalidEnvId(t *testing.T) {
	vars := map[string]string{"envId": "blah", "user": user()}
	req := buildRequest("GET", vars, nil, "", nil)
	resp := httptest.NewRecorder()
	clientSideHandler([]string{}).selectClientByUrlParam(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly(jsClientSdk))).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

func TestCorsMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildRequest("", nil, nil, "", nil)
	resp := httptest.NewRecorder()
	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "*")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Credentials"), "false")
		assert.Equal(t, w.Header().Get("Access-Control-Max-Age"), "300")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Headers"), "Cache-Control,Content-Type,Content-Length,Accept-Encoding,X-LaunchDarkly-User-Agent,X-LaunchDarkly-Payload-ID,X-LaunchDarkly-Wrapper,"+events.EventSchemaHeader)
		assert.Equal(t, w.Header().Get("Access-Control-Expose-Headers"), "Date")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectDefaultHeadersWhenRequestHasOrigin(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "", nil)
	resp := httptest.NewRecorder()

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectHeadersForSpecifiedDomain(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "", nil)
	resp := httptest.NewRecorder()

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)
}

func TestCorsMiddlewareSetsCorrectHeadersForInvalidOrigin(t *testing.T) {
	headers := map[string]string{"Origin": "blah"}
	req := buildRequest("", nil, headers, "", nil)
	resp := httptest.NewRecorder()

	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "blah")
	})).ServeHTTP(resp, req)

	handler().selectClientByAuthorizationKey(serverSdk)(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fail() })).ServeHTTP(resp, req)

}

type publishedEvent struct {
	url     string
	data    []byte
	authKey string
}

func makeTestEventBuffer(userKey string) []byte {
	event := map[string]interface{}{
		"kind":         "identify",
		"key":          userKey,
		"creationDate": 0,
		"user":         map[string]interface{}{"key": userKey},
	}
	data, _ := json.Marshal([]interface{}{event})
	return data
}

func makeTestFeatureEventPayload(userKey string) []byte {
	event := map[string]interface{}{
		"kind":         "feature",
		"creationDate": 0,
		"key":          "flag-key",
		"version":      1,
		"variation":    0,
		"value":        true,
		"userKey":      userKey,
	}
	data, _ := json.Marshal([]interface{}{event})
	return data
}

func TestRelay(t *testing.T) {
	publishedEvents := make(chan publishedEvent)

	requirePublishedEvent := func(data []byte) publishedEvent {
		timeout := time.After(time.Second * 3)
		select {
		case event := <-publishedEvents:
			assert.JSONEq(t, string(data), string(event.data))
			return event
		case <-timeout:
			assert.Fail(t, "did not get event within 3 seconds")
			return publishedEvent{} // won't get here
		}
	}

	eventsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, _ := ioutil.ReadAll(req.Body)
		publishedEvents <- publishedEvent{url: req.URL.String(), data: data, authKey: req.Header.Get("Authorization")}
		w.WriteHeader(http.StatusAccepted)
	}))

	sdkKey := c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d0")
	sdkKeyWithTTL := c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d5")
	sdkKeyClientSide := c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d1")
	sdkKeyMobile := c.SDKKey("sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d2")
	mobileKey := c.MobileKey("mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db")

	envId := c.EnvironmentID("507f1f77bcf86cd799439011")
	user := []byte(`{"key":"me"}`)
	base64User := base64.StdEncoding.EncodeToString([]byte(user))

	config := c.Config{
		Environment: map[string]*c.EnvConfig{
			"sdk test": {
				SDKKey: sdkKey,
			},
			"sdk test with TTL": {
				SDKKey: sdkKeyWithTTL,
				TTL:    c.NewOptDuration(10 * time.Minute),
			},
			"client-side test": {
				SDKKey: sdkKeyClientSide,
				EnvID:  envId,
			},
			"mobile test": {
				SDKKey:    sdkKeyMobile,
				MobileKey: mobileKey,
			},
		},
	}

	fakeApp := mux.NewRouter()
	fakeServer := httptest.NewServer(fakeApp)
	fakeServerURL, _ := url.Parse(fakeServer.URL)
	fakeApp.HandleFunc("/sdk/goals/{envId}", func(w http.ResponseWriter, req *http.Request) {
		ioutil.ReadAll(req.Body)
		if mux.Vars(req)["envId"] != string(envId) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.Equal(t, fakeServerURL.Hostname(), req.Host)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["got some goals"]`))
	}).Methods("GET")
	defer fakeServer.Close()

	config.Main.BaseURI, _ = c.NewOptAbsoluteURLFromString(fakeServer.URL)
	config.Events.SendEvents = true
	config.Events.EventsURI, _ = c.NewOptAbsoluteURLFromString(eventsServer.URL)
	config.Events.FlushInterval = c.NewOptDuration(time.Second)
	config.Events.Capacity = c.DefaultConfig.Events.Capacity

	createDummyClient := func(sdkKey c.SDKKey, config ld.Config) (sdkconfig.LDClientContext, error) {
		store, _ := config.DataStore.(*store.SSERelayDataStoreAdapter).CreateDataStore(
			testhelpers.NewSimpleClientContext(string(sdkKey)), nil)
		addAllFlags(store, true)
		return &fakeLDClient{true}, nil
	}

	relay, _ := NewRelay(config, logging.MakeDefaultLoggers(), createDummyClient)

	expectedJSEvalBody := expectJSONBody(makeEvalBody(clientSideFlags, false, false))
	expectedJSEvalxBody := expectJSONBody(makeEvalBody(clientSideFlags, true, false))
	expectedJSEvalxBodyWithReasons := expectJSONBody(makeEvalBody(clientSideFlags, true, true))
	expectedMobileEvalBody := expectJSONBody(makeEvalBody(allFlags, false, false))
	expectedMobileEvalxBody := expectJSONBody(makeEvalBody(allFlags, true, false))
	expectedMobileEvalxBodyWithReasons := expectJSONBody(makeEvalBody(allFlags, true, true))
	expectedFlagsData, _ := json.Marshal(flagsMap(allFlags))
	expectedAllData, _ := json.Marshal(map[string]map[string]interface{}{
		"data": {
			"flags": flagsMap(allFlags),
			"segments": map[string]interface{}{
				segment1.Key: &segment1,
			},
		},
	})

	getStatus := func(relay http.Handler, t *testing.T) map[string]interface{} {
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://localhost/status", nil)
		relay.ServeHTTP(w, r)
		result := w.Result()
		assert.Equal(t, http.StatusOK, result.StatusCode)
		body, _ := ioutil.ReadAll(result.Body)
		status := make(map[string]interface{})
		json.Unmarshal(body, &status)
		return status
	}

	assertNonStreamingHeaders := func(t *testing.T, h http.Header) {
		assert.Equal(t, "", h.Get("X-Accel-Buffering"))
		assert.NotRegexp(t, "^text/event-stream", h.Get("Content-Type"))
	}

	assertStreamingHeaders := func(t *testing.T, h http.Header) {
		assert.Equal(t, "no", h.Get("X-Accel-Buffering"))
		assert.Regexp(t, "^text/event-stream", h.Get("Content-Type"))
	}

	t.Run("status", func(t *testing.T) {
		status := getStatus(relay, t)
		assert.Equal(t, "sdk-********-****-****-****-*******e42d0", jsonFind(status, "environments", "sdk test", "sdkKey"))
		assert.Equal(t, "connected", jsonFind(status, "environments", "sdk test", "status"))
		assert.Equal(t, "sdk-********-****-****-****-*******e42d1", jsonFind(status, "environments", "client-side test", "sdkKey"))
		assert.Equal(t, "507f1f77bcf86cd799439011", jsonFind(status, "environments", "client-side test", "envId"))
		assert.Equal(t, "connected", jsonFind(status, "environments", "client-side test", "status"))
		assert.Equal(t, "sdk-********-****-****-****-*******e42d2", jsonFind(status, "environments", "mobile test", "sdkKey"))
		assert.Equal(t, "mob-********-****-****-****-*******e42db", jsonFind(status, "environments", "mobile test", "mobileKey"))
		assert.Equal(t, "connected", jsonFind(status, "environments", "mobile test", "status"))
		assert.Equal(t, "healthy", jsonFind(status, "status"))
		if assert.NotNil(t, jsonFind(status, "version")) {
			assert.NotEmpty(t, jsonFind(status, "version"))
		}
		if assert.NotNil(t, jsonFind(status, "clientVersion")) {
			assert.NotEmpty(t, jsonFind(status, "clientVersion"))
		}
	})

	t.Run("mobile routes", func(t *testing.T) {
		specs := []struct {
			name           string
			method         string
			path           string
			authHeader     c.SDKCredential
			body           []byte
			expectedStatus int
			bodyMatcher    bodyMatcher
		}{
			{"server-side report eval", "REPORT", "/sdk/eval/user", sdkKey, user, http.StatusOK, expectedMobileEvalBody},
			{"server-side report evalx", "REPORT", "/sdk/evalx/user", sdkKey, user, http.StatusOK, expectedMobileEvalxBody},
			{"server-side report evalx with reasons", "REPORT", "/sdk/evalx/user?withReasons=true", sdkKey, user, http.StatusOK, expectedMobileEvalxBodyWithReasons},
			{"mobile report eval", "REPORT", "/msdk/eval/user", mobileKey, user, http.StatusOK, expectedMobileEvalBody},
			{"mobile report evalx", "REPORT", "/msdk/evalx/user", mobileKey, user, http.StatusOK, expectedMobileEvalxBody},
			{"mobile report evalx with reasons", "REPORT", "/msdk/evalx/user?withReasons=true", mobileKey, user, http.StatusOK, expectedMobileEvalxBodyWithReasons},
			{"mobile get eval", "GET", fmt.Sprintf("/msdk/eval/users/%s", base64User), mobileKey, nil, http.StatusOK, nil},
			{"mobile get evalx", "GET", fmt.Sprintf("/msdk/evalx/users/%s", base64User), mobileKey, nil, http.StatusOK, nil},
		}

		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				var bodyBuffer io.Reader
				if s.body != nil {
					bodyBuffer = bytes.NewBuffer(s.body)
				}
				r, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
				r.Header.Set("Content-Type", "application/json")
				if s.authHeader.GetAuthorizationHeaderValue() != "" {
					r.Header.Set("Authorization", s.authHeader.GetAuthorizationHeaderValue())
				}
				relay.ServeHTTP(w, r)
				result := w.Result()
				assert.Equal(t, s.expectedStatus, result.StatusCode)
				if s.bodyMatcher != nil {
					body, _ := ioutil.ReadAll(result.Body)
					s.bodyMatcher(t, body)
				}
				assertNonStreamingHeaders(t, w.Header())
			})
		}
	})

	t.Run("sdk and mobile streams", func(t *testing.T) {
		specs := []struct {
			name          string
			method        string
			path          string
			authHeader    c.SDKCredential
			body          []byte
			expectedEvent string
			expectedData  []byte
		}{
			{"flags stream", "GET", "/flags", sdkKey, nil, "put", expectedFlagsData},
			{"all stream", "GET", "/all", sdkKey, nil, "put", expectedAllData},
			{"mobile ping", "GET", "/mping", mobileKey, nil, "ping", nil},
			{"mobile stream GET", "GET", "/meval/" + base64User, mobileKey, nil, "ping", nil},
			{"mobile stream REPORT", "REPORT", "/meval", mobileKey, user, "ping", nil},
		}

		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				w, bodyReader := NewStreamRecorder()
				bodyBuffer := bytes.NewBuffer(s.body)
				r, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
				r.Header.Set("Content-Type", "application/json")
				if s.authHeader.GetAuthorizationHeaderValue() != "" {
					r.Header.Set("Authorization", s.authHeader.GetAuthorizationHeaderValue())
				}
				wg := sync.WaitGroup{}
				wg.Add(1)
				go func() {
					relay.ServeHTTP(w, r)
					wg.Done()
				}()
				dec := eventsource.NewDecoder(bodyReader)
				eventCh := make(chan eventsource.Event, 1)
				go func() {
					event, err := dec.Decode()
					assert.NoError(t, err)
					eventCh <- event
				}()
				select {
				case event := <-eventCh:
					if event != nil {
						assert.Equal(t, s.expectedEvent, event.Event())
						if s.expectedData != nil {
							assert.JSONEq(t, string(s.expectedData), event.Data())
						}
						assertStreamingHeaders(t, w.Header())
					}
				case <-time.After(time.Second * 3):
					assert.Fail(t, "timed out waiting for event")
				}
				w.Close()
				wg.Wait()
			})
		}
	})

	t.Run("client-side routes", func(t *testing.T) {
		base64Events := base64.StdEncoding.EncodeToString([]byte(`[]`))
		specs := []struct {
			name           string
			method         string
			path           string
			body           []byte
			expectedStatus int
			bodyMatcher    bodyMatcher
		}{
			{"report eval ", "REPORT", fmt.Sprintf("/sdk/eval/%s/user", envId), user, http.StatusOK, expectedJSEvalBody},
			{"report evalx", "REPORT", fmt.Sprintf("/sdk/evalx/%s/user", envId), user, http.StatusOK, expectedJSEvalxBody},
			{"report evalx with reasons", "REPORT", fmt.Sprintf("/sdk/evalx/%s/user?withReasons=true", envId), user, http.StatusOK, expectedJSEvalxBodyWithReasons},
			{"get eval", "GET", fmt.Sprintf("/sdk/eval/%s/users/%s", envId, base64User), nil, http.StatusOK, expectedJSEvalBody},
			{"get evalx", "GET", fmt.Sprintf("/sdk/evalx/%s/users/%s", envId, base64User), nil, http.StatusOK, expectedJSEvalxBody},
			{"get evalx with reasons", "GET", fmt.Sprintf("/sdk/evalx/%s/users/%s?withReasons=true", envId, base64User), nil, http.StatusOK, expectedJSEvalxBodyWithReasons},
			{"post events", "POST", fmt.Sprintf("/events/bulk/%s", envId), []byte("[]"), http.StatusAccepted, nil},
			{"get events image", "GET", fmt.Sprintf("/a/%s.gif?d=%s", envId, base64Events), nil, http.StatusOK, expectBody(string(transparent1PixelImg))},
			{"get goals", "GET", fmt.Sprintf("/sdk/goals/%s", envId), nil, http.StatusOK, expectBody(`["got some goals"]`)},
		}

		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("requests", func(t *testing.T) {
					w := httptest.NewRecorder()
					var bodyBuffer io.Reader
					if s.body != nil {
						bodyBuffer = bytes.NewBuffer(s.body)
					}
					r, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
					r.Header.Set("Content-Type", "application/json")
					relay.ServeHTTP(w, r)
					result := w.Result()
					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(result.Header.Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "*", result.Header.Get("Access-Control-Allow-Origin"))
					}
					if s.bodyMatcher != nil {
						body, _ := ioutil.ReadAll(w.Result().Body)
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
					assertNonStreamingHeaders(t, w.Header())
				})

				t.Run("options", func(t *testing.T) {
					w := httptest.NewRecorder()
					r, _ := http.NewRequest("OPTIONS", "http://localhost"+s.path, nil)
					relay.ServeHTTP(w, r)
					result := w.Result()
					if assert.Equal(t, http.StatusOK, result.StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(result.Header.Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "*", result.Header.Get("Access-Control-Allow-Origin"))
					}
				})

				t.Run("options with host", func(t *testing.T) {
					w := httptest.NewRecorder()
					r, _ := http.NewRequest("OPTIONS", "http://localhost"+s.path, nil)
					r.Header.Set("Origin", "my-host.com")
					relay.ServeHTTP(w, r)
					result := w.Result()
					if assert.Equal(t, http.StatusOK, result.StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(result.Header.Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "my-host.com", result.Header.Get("Access-Control-Allow-Origin"))
					}
				})
			})
		}
	})

	t.Run("client-side streams", func(t *testing.T) {
		specs := []struct {
			name          string
			method        string
			path          string
			body          []byte
			expectedEvent string
		}{
			{"client-side get ping", "GET", fmt.Sprintf("/ping/%s", envId), nil, "ping"},
			{"client-side get eval stream", "GET", fmt.Sprintf("/eval/%s/%s", envId, base64User), nil, "ping"},
			{"client-side report eval stream", "REPORT", fmt.Sprintf("/eval/%s", envId), user, "ping"},
		}

		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("requests", func(t *testing.T) {
					w, bodyReader := NewStreamRecorder()
					bodyBuffer := bytes.NewBuffer(s.body)
					r, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
					r.Header.Set("Content-Type", "application/json")
					wg := sync.WaitGroup{}
					wg.Add(1)
					go func() {
						relay.ServeHTTP(w, r)
						wg.Done()
					}()
					dec := eventsource.NewDecoder(bodyReader)
					event, err := dec.Decode()
					if assert.NoError(t, err) {
						assert.Equal(t, s.expectedEvent, event.Event())
					}
					w.Close()
					wg.Wait()
					result := w.Result()
					assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(result.Header.Get("Access-Control-Allow-Methods"), ","))
					assert.Equal(t, "*", result.Header.Get("Access-Control-Allow-Origin"))
					assertStreamingHeaders(t, w.Header())
				})

				t.Run("options", func(t *testing.T) {
					w := httptest.NewRecorder()
					r, _ := http.NewRequest("OPTIONS", "http://localhost"+s.path, nil)
					relay.ServeHTTP(w, r)
					result := w.Result()
					assert.Equal(t, http.StatusOK, result.StatusCode)
					assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(result.Header.Get("Access-Control-Allow-Methods"), ","))
					assert.Equal(t, "*", result.Header.Get("Access-Control-Allow-Origin"))
				})

				t.Run("options with host", func(t *testing.T) {
					w := httptest.NewRecorder()
					r, _ := http.NewRequest("OPTIONS", "http://localhost"+s.path, nil)
					r.Header.Set("Origin", "my-host.com")
					relay.ServeHTTP(w, r)
					result := w.Result()
					if assert.Equal(t, http.StatusOK, result.StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(result.Header.Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "my-host.com", result.Header.Get("Access-Control-Allow-Origin"))
					}
				})
			})
		}
	})

	t.Run("server-side events proxies", func(t *testing.T) {
		t.Run("bulk post", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestFeatureEventPayload("me")
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest("POST", "http://localhost/bulk", bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", string(sdkKey))
			r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, "/bulk", event.url)
				assert.Equal(t, string(sdkKey), event.authKey)
			}
		})

		t.Run("diagnostics forwarding", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestFeatureEventPayload("me")
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest("POST", "http://localhost/diagnostic", bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", string(sdkKey))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, "/diagnostic", event.url)
				assert.Equal(t, string(sdkKey), event.authKey)
			}
		})
	})

	t.Run("mobile events proxies", func(t *testing.T) {
		specs := []struct {
			name   string
			method string
			path   string
		}{
			{"mobile events", "POST", "/mobile/events"},
			{"mobile events bulk", "POST", "/mobile/events/bulk"},
		}

		for i, s := range specs {
			w := httptest.NewRecorder()
			body := makeTestFeatureEventPayload(fmt.Sprintf("me%d", i))
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", string(mobileKey))
			r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, "/mobile", event.url)
				assert.Equal(t, string(mobileKey), event.authKey)
			}
		}

		t.Run("diagnostics forwarding", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestFeatureEventPayload("me")
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest("POST", "http://localhost/mobile/events/diagnostic", bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", string(mobileKey))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, "/mobile/events/diagnostic", event.url)
				assert.Equal(t, string(mobileKey), event.authKey)
			}
		})
	})

	t.Run("client-side events proxies", func(t *testing.T) {
		expectedPath := "/events/bulk/" + string(envId)

		t.Run("bulk post", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestFeatureEventPayload("me-post")
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest("POST", fmt.Sprintf("http://localhost/events/bulk/%s", envId), bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, expectedPath, event.url)
				assert.Equal(t, "", event.authKey)
			}
		})

		t.Run("image", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestFeatureEventPayload("me-image")
			base64Body := base64.StdEncoding.EncodeToString(body)
			r, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost/a/%s.gif?d=%s", envId, base64Body), nil)
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusOK, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, expectedPath, event.url)
				assert.Equal(t, "", event.authKey)
			}
		})

		t.Run("diagnostics forwarding", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestEventBuffer("me")
			bodyBuffer := bytes.NewBuffer(body)
			expectedPath := fmt.Sprintf("/events/diagnostic/%s", envId)
			r, _ := http.NewRequest("POST", "http://localhost"+expectedPath, bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := requirePublishedEvent(body)
				assert.Equal(t, expectedPath, event.url)
				assert.Equal(t, "", event.authKey)
			}
		})
	})

	t.Run("PHP endpoints", func(t *testing.T) {
		makeRequest := func(url string) *http.Request {
			r, _ := http.NewRequest("GET", url, nil)
			r.Header.Set("Authorization", string(sdkKey))
			return r
		}

		assertOKResponseWithEntity := func(t *testing.T, resp *http.Response, entity interface{}) {
			if assert.Equal(t, http.StatusOK, resp.StatusCode) {
				body, _ := ioutil.ReadAll(resp.Body)
				expectedJson, _ := json.Marshal(entity)
				assert.Equal(t, string(expectedJson), string(body))
			}
		}

		assertQueryWithSameEtagIsCached := func(t *testing.T, req *http.Request, resp *http.Response) {
			if assert.Equal(t, http.StatusOK, resp.StatusCode) {
				etag := resp.Header.Get("Etag")
				if assert.NotEqual(t, "", etag) {
					w := httptest.NewRecorder()
					req.Header.Set("If-None-Match", etag)
					relay.ServeHTTP(w, req)
					assert.Equal(t, http.StatusNotModified, w.Result().StatusCode)
				}
			}
		}

		assertQueryWithDifferentEtagIsNotCached := func(t *testing.T, req *http.Request, resp *http.Response) {
			if assert.Equal(t, http.StatusOK, resp.StatusCode) {
				etag := resp.Header.Get("Etag")
				if assert.NotEqual(t, "", etag) {
					w := httptest.NewRecorder()
					req.Header.Set("If-None-Match", "different-from-"+etag)
					relay.ServeHTTP(w, req)
					assert.Equal(t, http.StatusOK, w.Result().StatusCode)
				}
			}
		}

		t.Run("get flag", func(t *testing.T) {
			w := httptest.NewRecorder()
			r := makeRequest(fmt.Sprintf("http://localhost/sdk/flags/%s", flag1ServerSide.flag.Key))
			relay.ServeHTTP(w, r)
			assertOKResponseWithEntity(t, w.Result(), flag1ServerSide.flag)
			assert.Equal(t, "", w.Result().Header.Get("Expires")) // TTL isn't set for this environment
			assertQueryWithSameEtagIsCached(t, r, w.Result())
			assertQueryWithDifferentEtagIsNotCached(t, r, w.Result())
			assertNonStreamingHeaders(t, w.Header())
		})

		t.Run("get flag - not found", func(t *testing.T) {
			w := httptest.NewRecorder()
			r := makeRequest("http://localhost/sdk/flags/no-such-flag")
			relay.ServeHTTP(w, r)
			assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
		})

		t.Run("get flag - environment has TTL", func(t *testing.T) {
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost/sdk/flags/%s", flag1ServerSide.flag.Key), nil)
			r.Header.Set("Authorization", string(sdkKeyWithTTL))
			relay.ServeHTTP(w, r)
			assertOKResponseWithEntity(t, w.Result(), flag1ServerSide.flag)
			assert.NotEqual(t, "", w.Result().Header.Get("Expires"))
		})

		t.Run("get all flags", func(t *testing.T) {
			w := httptest.NewRecorder()
			r := makeRequest("http://localhost/sdk/flags")
			relay.ServeHTTP(w, r)
			assertOKResponseWithEntity(t, w.Result(), flagsMap(allFlags))
			assert.Equal(t, "", w.Result().Header.Get("Expires")) // TTL isn't set for this environment
			assertQueryWithSameEtagIsCached(t, r, w.Result())
			assertQueryWithDifferentEtagIsNotCached(t, r, w.Result())
			assertNonStreamingHeaders(t, w.Header())
		})

		t.Run("get segment", func(t *testing.T) {
			w := httptest.NewRecorder()
			r := makeRequest(fmt.Sprintf("http://localhost/sdk/segments/%s", segment1.Key))
			relay.ServeHTTP(w, r)
			assertOKResponseWithEntity(t, w.Result(), segment1)
			assert.Equal(t, "", w.Result().Header.Get("Expires")) // TTL isn't set for this environment
			assertQueryWithSameEtagIsCached(t, r, w.Result())
			assertQueryWithDifferentEtagIsNotCached(t, r, w.Result())
			assertNonStreamingHeaders(t, w.Header())
		})

		t.Run("get segment - not found", func(t *testing.T) {
			w := httptest.NewRecorder()
			r := makeRequest("http://localhost/sdk/segments/no-such-segment")
			relay.ServeHTTP(w, r)
			assert.Equal(t, http.StatusNotFound, w.Result().StatusCode)
		})
	})

	eventsServer.Close()
}

func TestGetUserAgent(t *testing.T) {
	t.Run("X-LaunchDarkly-User-Agent takes precedence", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		req.Header.Set(ldUserAgentHeader, "my-agent")
		req.Header.Set(userAgentHeader, "something-else")
		assert.Equal(t, "my-agent", getUserAgent(req))
	})
	t.Run("User-Agent is the fallback", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/", nil)
		req.Header.Set(userAgentHeader, "my-agent")
		assert.Equal(t, "my-agent", getUserAgent(req))
	})
}
