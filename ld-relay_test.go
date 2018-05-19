package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	ld "gopkg.in/launchdarkly/go-client.v4"
)

type FakeLDClient struct{ initialized bool }

func (c FakeLDClient) Initialized() bool {
	return c.initialized
}

var nullLogger = log.New(ioutil.Discard, "", 0)
var emptyStore = ld.NewInMemoryFeatureStore(nullLogger)

// Returns a key matching the UUID header pattern
func key() string {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

func handler() ClientMux {
	clients := map[string]clientContext{key(): &clientContextImpl{client: FakeLDClient{}, store: emptyStore, logger: nullLogger}}
	return ClientMux{clientContextByKey: clients}
}

func clientSideHandler(allowedOrigins []string) ClientSideMux {
	testClientSideContext := &clientSideContext{allowedOrigins: allowedOrigins, clientContext: &clientContextImpl{client: FakeLDClient{}, store: emptyStore, logger: nullLogger}}
	contexts := map[string]*clientSideContext{key(): testClientSideContext}
	return ClientSideMux{contextByKey: contexts}
}

func buildRequest(verb string, vars map[string]string, headers map[string]string, body string, ctx interface{}) *http.Request {
	req, _ := http.NewRequest(verb, "", bytes.NewBufferString(body))
	req = mux.SetURLVars(req, vars)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), "context", ctx))
	return req
}

func makeStoreWithData(initialized bool) ld.FeatureStore {
	zero := 0
	store := ld.NewInMemoryFeatureStore(nullLogger)
	if initialized {
		store.Init(nil)
	}
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "another-flag-key", On: true, Fallthrough: ld.VariationOrRollout{Variation: &zero}, Variations: []interface{}{3}, Version: 1})
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "some-flag-key", OffVariation: &zero, Variations: []interface{}{true}, Version: 2})
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "off-variation-key", Version: 3})
	return store
}

func makeTestContextWithData() *clientContextImpl {
	return &clientContextImpl{
		client: FakeLDClient{initialized: true},
		store:  makeStoreWithData(true),
		logger: nullLogger,
	}
}

func TestGetFlagEvalValueOnlySucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true, "off-variation-key": null}`, string(b))
}

func TestReportFlagEvalValueOnlySucceeds(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true, "off-variation-key": null}`, string(b))
}

func TestGetFlagEvalSucceeds(t *testing.T) {
	vars := map[string]string{"user": user()}
	req := buildRequest("GET", vars, nil, "", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{
"another-flag-key":{"value": 3, "variation": 0, "version" :1, "trackEvents": false},
"some-flag-key":{"value": true, "variation": 0, "version": 2, "trackEvents": false},
"off-variation-key":{"value": null, "version": 3, "trackEvents": false}
}`, string(b))
}

func TestReportFlagEvalSucceeds(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{
"another-flag-key":{"value": 3, "variation": 0, "version" :1, "trackEvents": false},
"some-flag-key":{"value": true, "variation": 0, "version": 2, "trackEvents": false},
"off-variation-key":{"value": null, "version": 3, "trackEvents": false}
}`, string(b))
}

func TestAuthorizeMethodFailsOnInvalidAuthKey(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Authorization": "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee", "Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}`, nil)
	resp := httptest.NewRecorder()
	handler().selectClientByAuthorizationKey(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fail() })).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusUnauthorized, resp.Code)
}

func TestFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	vars := map[string]string{"user": user()}
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", vars, headers, `{"user":"key"}notjson`, nil)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)
}

func TestReportFlagEvalFailsWithMissingUserKey(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	req := buildRequest("REPORT", nil, headers, "{}", makeTestContextWithData())
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusBadRequest, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"User must have a 'key' attribute"}`, string(b))
}

func TestReportFlagEvalFailsallowMethodOptionsHandlerWithUninitializedClientAndStore(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	ctx := &clientContextImpl{
		client: FakeLDClient{initialized: false},
		store:  makeStoreWithData(false),
		logger: nullLogger,
	}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlags(resp, req)

	assert.Equal(t, http.StatusServiceUnavailable, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)

	assert.JSONEq(t, `{"message":"Service not initialized"}`, string(b))
}

func TestReportFlagEvalWorksWithUninitializedClientButInitializedStore(t *testing.T) {
	headers := map[string]string{"Content-Type": "application/json"}
	ctx := &clientContextImpl{
		client: FakeLDClient{initialized: false},
		store:  makeStoreWithData(true),
		logger: nullLogger,
	}
	req := buildRequest("REPORT", nil, headers, `{"key": "my-user"}`, ctx)
	resp := httptest.NewRecorder()
	evaluateAllFeatureFlagsValueOnly(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)

	b, _ := ioutil.ReadAll(resp.Body)
	assert.JSONEq(t, `{"another-flag-key":3,"some-flag-key":true, "off-variation-key": null}`, string(b))
}

func TestFindEnvironmentFailsOnInvalidEnvId(t *testing.T) {
	vars := map[string]string{"envId": "blah", "user": user()}
	req := buildRequest("GET", vars, nil, "", nil)
	resp := httptest.NewRecorder()
	clientSideHandler([]string{}).selectClientByUrlParam(http.HandlerFunc(evaluateAllFeatureFlagsValueOnly)).ServeHTTP(resp, req)

	assert.Equal(t, http.StatusNotFound, resp.Code)
}

func TestCorsMiddlewareSetsCorrectDefaultHeaders(t *testing.T) {
	req := buildRequest("", nil, nil, "", nil)
	resp := httptest.NewRecorder()
	corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Origin"), "*")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Credentials"), "false")
		assert.Equal(t, w.Header().Get("Access-Control-Max-Age"), "300")
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Headers"), "Content-Type, Content-Length, Accept-Encoding, X-LaunchDarkly-User-Agent")
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

	handler().selectClientByAuthorizationKey(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fail() })).ServeHTTP(resp, req)

}

type bodyMatcher func(t *testing.T, body []byte)

func expectBody(expectedBody string) bodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.EqualValues(t, expectedBody, body)
	}
}

func expectJSONBody(expectedBody string) bodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.JSONEq(t, expectedBody, string(body))
	}
}

func TestRouter(t *testing.T) {
	logger := log.New(os.Stderr, "", 0)
	sdkKey := "sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"
	mobileKey := "mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"

	okHandler := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
			w.WriteHeader(http.StatusOK)
		})
	}

	expectedFlagsStream := expectBody("flags")
	expectedAllStream := expectBody("all")
	expectedPingStream := expectBody("ping")
	expectedEventsBody := expectBody("events")

	handlers := clientHandlers{
		flagsStreamHandler: okHandler("flags"),
		allStreamHandler:   okHandler("all"),
		pingStreamHandler:  okHandler("ping"),
		eventsHandler:      okHandler("events"),
	}

	store := ld.NewInMemoryFeatureStore(logger)
	store.Init(nil)
	zero := 0
	store.Upsert(ld.Features, &ld.FeatureFlag{Key: "my-flag", OffVariation: &zero, Variations: []interface{}{1}})
	client := clientContextImpl{client: &FakeLDClient{true}, store: store, logger: logger, handlers: handlers}
	jsClient := clientSideContext{clientContext: &client}
	sdkClientMux := ClientMux{clientContextByKey: map[string]clientContext{sdkKey: &client}}
	mobileClientMux := ClientMux{clientContextByKey: map[string]clientContext{mobileKey: &client}}

	envId := "507f1f77bcf86cd799439011"
	clientSideMux := ClientSideMux{contextByKey: map[string]*clientSideContext{envId: &jsClient}}
	user := []byte(`{"key":"me"}`)
	base64User := base64.StdEncoding.EncodeToString([]byte(user))

	r := relay{
		sdkClientMux:    sdkClientMux,
		mobileClientMux: mobileClientMux,
		clientSideMux:   clientSideMux,
	}
	router := r.newRouter()

	expectedEvalBody := expectJSONBody(`{"my-flag":1}`)
	expectedEvalxBody := expectJSONBody(`{"my-flag":{"value":1,"variation":0,"version":0,"trackEvents":false}}`)

	t.Run("routes", func(t *testing.T) {
		specs := []struct {
			name           string
			method         string
			path           string
			authHeader     string
			body           []byte
			expectedStatus int
			bodyMatcher    bodyMatcher
		}{
			{"status", "GET", "/status", "", nil, http.StatusOK, expectJSONBody(`{"environments":{"sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42db":{"status":"connected"}}, "status":"healthy"}`)},
			{"server-side report eval", "REPORT", "/sdk/eval/user", sdkKey, user, http.StatusOK, expectedEvalBody},
			{"server-side report evalx", "REPORT", "/sdk/evalx/user", sdkKey, user, http.StatusOK, expectedEvalxBody},
			{"flags stream", "GET", "/flags", sdkKey, nil, http.StatusOK, expectedFlagsStream},
			{"all stream", "GET", "/all", sdkKey, nil, http.StatusOK, expectedAllStream},
			{"events bulk", "POST", "/bulk", sdkKey, nil, http.StatusOK, expectedEventsBody},
			{"mobile events", "POST", "/mobile/events", mobileKey, nil, http.StatusOK, expectedEventsBody},
			{"mobile events bulk", "POST", "/mobile/events/bulk", mobileKey, nil, http.StatusOK, expectedEventsBody},
			{"mobile report eval", "REPORT", "/msdk/eval/user", mobileKey, user, http.StatusOK, expectedEvalBody},
			{"mobile report evalx", "REPORT", "/msdk/evalx/user", mobileKey, user, http.StatusOK, expectedEvalxBody},
			{"mobile get eval", "GET", fmt.Sprintf("/msdk/eval/users/%s", base64User), mobileKey, nil, http.StatusOK, nil},
			{"mobile get evalx", "GET", fmt.Sprintf("/msdk/evalx/users/%s", base64User), mobileKey, nil, http.StatusOK, nil},
			{"mobile ping", "GET", "/mping", mobileKey, nil, http.StatusOK, nil},
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
				if s.authHeader != "" {
					r.Header.Set("Authorization", s.authHeader)
				}
				router.ServeHTTP(w, r)
				assert.Equal(t, s.expectedStatus, w.Result().StatusCode)
				if s.bodyMatcher != nil {
					body, _ := ioutil.ReadAll(w.Result().Body)
					s.bodyMatcher(t, body)
				}
			})
		}
	})

	t.Run("client-side routes", func(t *testing.T) {
		specs := []struct {
			name           string
			method         string
			path           string
			body           []byte
			expectedStatus int
			bodyMatcher    bodyMatcher
		}{
			{"client-side report eval ", "REPORT", fmt.Sprintf("/sdk/eval/%s/user", envId), user, http.StatusOK, expectedEvalBody},
			{"client-side report evalx", "REPORT", fmt.Sprintf("/sdk/evalx/%s/user", envId), user, http.StatusOK, expectedEvalxBody},
			{"client-side get eval", "GET", fmt.Sprintf("/sdk/eval/%s/users/%s", envId, base64User), nil, http.StatusOK, expectedEvalBody},
			{"client-side get evalx", "GET", fmt.Sprintf("/sdk/evalx/%s/users/%s", envId, base64User), nil, http.StatusOK, expectedEvalxBody},
			{"client-side get ping", "GET", fmt.Sprintf("/ping/%s", envId), nil, http.StatusOK, nil},
			{"client-side post events", "POST", fmt.Sprintf("/events/bulk/%s", envId), nil, http.StatusOK, nil},
			{"client-side get events image", "GET", fmt.Sprintf("/a/%s.gif", envId), nil, http.StatusOK, expectBody(string(transparent1PixelImg))},
			{"client-side get eval stream", "GET", fmt.Sprintf("/eval/%s/%s", envId, base64User), nil, http.StatusOK, expectedPingStream},
			{"client-side report eval stream", "REPORT", fmt.Sprintf("/eval/%s", envId), user, http.StatusOK, expectedPingStream},
		}

		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("requests", func(t *testing.T) {
					w := httptest.NewRecorder()
					var bodyBuffer io.Reader
					if s.body != nil {
						bodyBuffer = bytes.NewBuffer(s.body)
					}
					request, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(w, request)
					if assert.Equal(t, s.expectedStatus, w.Result().StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(w.Header().Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
					}
					if s.bodyMatcher != nil {
						body, _ := ioutil.ReadAll(w.Result().Body)
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("options", func(t *testing.T) {
					w := httptest.NewRecorder()
					request, _ := http.NewRequest("OPTIONS", "http://localhost"+s.path, nil)
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(w, request)
					assert.Equal(t, http.StatusOK, w.Result().StatusCode)
					if assert.Equal(t, s.expectedStatus, w.Result().StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(w.Header().Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
					}
				})

				t.Run("options with host", func(t *testing.T) {
					w := httptest.NewRecorder()
					request, _ := http.NewRequest("OPTIONS", "http://localhost"+s.path, nil)
					request.Header.Set("Origin", "my-host.com")
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(w, request)
					if assert.Equal(t, http.StatusOK, w.Result().StatusCode) {
						assert.ElementsMatch(t, []string{s.method, "OPTIONS", "OPTIONS"}, strings.Split(w.Header().Get("Access-Control-Allow-Methods"), ","))
						assert.Equal(t, "my-host.com", w.Header().Get("Access-Control-Allow-Origin"))
					}
				})
			})
		}
	})
}
