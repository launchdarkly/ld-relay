package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/eventsource"
	ld "gopkg.in/launchdarkly/go-client.v4"

	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
	"gopkg.in/launchdarkly/ld-relay.v5/logging"
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

func handler() clientMux {
	clients := map[string]*clientContextImpl{key(): {client: FakeLDClient{}, store: emptyStore, logger: nullLogger}}
	return clientMux{clientContextByKey: clients}
}

func clientSideHandler(allowedOrigins []string) clientSideMux {
	testClientSideContext := &clientSideContext{allowedOrigins: allowedOrigins, clientContext: &clientContextImpl{client: FakeLDClient{}, store: emptyStore, logger: nullLogger}}
	contexts := map[string]*clientSideContext{key(): testClientSideContext}
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
		assert.Equal(t, w.Header().Get("Access-Control-Allow-Headers"), "Content-Type,Content-Length,Accept-Encoding,X-LaunchDarkly-User-Agent,"+events.EventSchemaHeader)
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

type StreamRecorder struct {
	*bufio.Writer
	*httptest.ResponseRecorder
	closer chan bool
}

func (r StreamRecorder) CloseNotify() <-chan bool {
	return r.closer
}

func (r StreamRecorder) Close() {
	r.closer <- true
}

func (r StreamRecorder) Write(data []byte) (int, error) {
	return r.Writer.Write(data)
}

func (r StreamRecorder) Flush() {
	r.Writer.Flush()
}

func NewStreamRecorder() (StreamRecorder, io.Reader) {
	reader, writer := io.Pipe()
	recorder := httptest.NewRecorder()
	return StreamRecorder{
		ResponseRecorder: recorder,
		Writer:           bufio.NewWriter(writer),
		closer:           make(chan bool),
	}, reader
}

type publishedEvent struct {
	url  string
	data []byte
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

func TestRelay(t *testing.T) {
	logging.InitLogging(ioutil.Discard, os.Stdout, os.Stdout, os.Stderr)

	publishedEvents := make(chan publishedEvent)

	expectEventBuffer := func(data []byte) {
		timeout := time.After(time.Second * 10)
		select {
		case event := <-publishedEvents:
			assert.JSONEq(t, string(data), string(event.data))
			assert.Equal(t, "/bulk", event.url)
		case <-timeout:
			assert.Fail(t, "did not get event within 3 seconds")
		}
	}

	eventsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, _ := ioutil.ReadAll(req.Body)
		publishedEvents <- publishedEvent{url: req.URL.String(), data: data}
		w.WriteHeader(http.StatusAccepted)
	}))

	sdkKey := "sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d0"
	sdkKeyClientSide := "sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d1"
	sdkKeyMobile := "sdk-98e2b0b4-2688-4a59-9810-1e0e3d7e42d2"
	mobileKey := "mob-98e2b0b4-2688-4a59-9810-1e0e3d7e42db"

	envId := "507f1f77bcf86cd799439011"
	user := []byte(`{"key":"me"}`)
	base64User := base64.StdEncoding.EncodeToString([]byte(user))

	config := Config{
		Environment: map[string]*EnvConfig{
			"sdk test": {
				SdkKey: sdkKey,
			},
			"client-side test": {
				SdkKey: sdkKeyClientSide,
				EnvId:  &envId,
			},
			"mobile test": {
				SdkKey:    sdkKeyMobile,
				MobileKey: &mobileKey,
			},
		},
	}

	fakeApp := mux.NewRouter()
	fakeServer := httptest.NewServer(fakeApp)
	fakeServerURL, _ := url.Parse(fakeServer.URL)
	fakeApp.HandleFunc("/sdk/goals/{envId}", func(w http.ResponseWriter, req *http.Request) {
		ioutil.ReadAll(req.Body)
		if mux.Vars(req)["envId"] != envId {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.Equal(t, fakeServerURL.Hostname(), req.Host)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["got some goals"]`))
	}).Methods("GET")
	defer fakeServer.Close()

	config.Main.BaseUri = fakeServer.URL
	config.Events.SendEvents = true
	config.Events.EventsUri = eventsServer.URL
	config.Events.FlushIntervalSecs = 1
	config.Events.Capacity = defaultEventCapacity

	zero := 0
	flag := ld.FeatureFlag{Key: "my-flag", OffVariation: &zero, Variations: []interface{}{1}}

	createDummyClient := func(sdkKey string, config ld.Config) (LdClientContext, error) {
		config.FeatureStore.Init(nil)
		config.FeatureStore.Upsert(ld.Features, &flag)
		return &FakeLDClient{true}, nil
	}

	relay, _ := NewRelay(config, createDummyClient)

	expectedEvalBody := expectJSONBody(`{"my-flag":1}`)
	expectedEvalxBody := expectJSONBody(`{"my-flag":{"value":1,"variation":0,"version":0,"trackEvents":false}}`)
	allFlags := map[string]interface{}{"my-flag": flag}
	expectedFlagsData, _ := json.Marshal(allFlags)
	expectedAllData, _ := json.Marshal(map[string]map[string]interface{}{"data": {"flags": allFlags, "segments": map[string]interface{}{}}})

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

	t.Run("sdk and mobile routes", func(t *testing.T) {
		specs := []struct {
			name           string
			method         string
			path           string
			authHeader     string
			body           []byte
			expectedStatus int
			bodyMatcher    bodyMatcher
		}{
			{"server-side report eval", "REPORT", "/sdk/eval/user", sdkKey, user, http.StatusOK, expectedEvalBody},
			{"server-side report evalx", "REPORT", "/sdk/evalx/user", sdkKey, user, http.StatusOK, expectedEvalxBody},
			{"mobile report eval", "REPORT", "/msdk/eval/user", mobileKey, user, http.StatusOK, expectedEvalBody},
			{"mobile report evalx", "REPORT", "/msdk/evalx/user", mobileKey, user, http.StatusOK, expectedEvalxBody},
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
				if s.authHeader != "" {
					r.Header.Set("Authorization", s.authHeader)
				}
				relay.ServeHTTP(w, r)
				result := w.Result()
				assert.Equal(t, s.expectedStatus, result.StatusCode)
				if s.bodyMatcher != nil {
					body, _ := ioutil.ReadAll(result.Body)
					s.bodyMatcher(t, body)
				}
			})
		}
	})

	t.Run("sdk and mobile streams", func(t *testing.T) {
		specs := []struct {
			name          string
			method        string
			path          string
			authHeader    string
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
				if s.authHeader != "" {
					r.Header.Set("Authorization", s.authHeader)
				}
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
					if s.expectedData != nil {
						assert.JSONEq(t, string(s.expectedData), event.Data())
					}
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
			{"report eval ", "REPORT", fmt.Sprintf("/sdk/eval/%s/user", envId), user, http.StatusOK, expectedEvalBody},
			{"report evalx", "REPORT", fmt.Sprintf("/sdk/evalx/%s/user", envId), user, http.StatusOK, expectedEvalxBody},
			{"get eval", "GET", fmt.Sprintf("/sdk/eval/%s/users/%s", envId, base64User), nil, http.StatusOK, expectedEvalBody},
			{"get evalx", "GET", fmt.Sprintf("/sdk/evalx/%s/users/%s", envId, base64User), nil, http.StatusOK, expectedEvalxBody},
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

	t.Run("server-side and mobile events proxies", func(t *testing.T) {
		specs := []struct {
			name       string
			method     string
			path       string
			authHeader string
		}{
			{"events bulk", "POST", "/bulk", sdkKey},
			{"mobile events", "POST", "/mobile/events", mobileKey},
			{"mobile events bulk", "POST", "/mobile/events/bulk", mobileKey},
		}

		for i, s := range specs {
			w := httptest.NewRecorder()
			body := makeTestEventBuffer(fmt.Sprintf("me%d", i))
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest(s.method, "http://localhost"+s.path, bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", s.authHeader)
			r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				expectEventBuffer(body)
			}
		}
	})

	t.Run("client-side events proxies", func(t *testing.T) {
		t.Run("bulk post", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestEventBuffer("me-post")
			bodyBuffer := bytes.NewBuffer(body)
			r, _ := http.NewRequest("POST", fmt.Sprintf("http://localhost/events/bulk/%s", envId), bodyBuffer)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				expectEventBuffer(body)
			}
		})

		t.Run("image", func(t *testing.T) {
			w := httptest.NewRecorder()
			body := makeTestEventBuffer("me-image")
			base64Body := base64.StdEncoding.EncodeToString(body)
			r, _ := http.NewRequest("GET", fmt.Sprintf("http://localhost/a/%s.gif?d=%s", envId, base64Body), nil)
			relay.ServeHTTP(w, r)
			result := w.Result()
			if assert.Equal(t, http.StatusOK, result.StatusCode) {
				expectEventBuffer(body)
			}
		})
	})

	eventsServer.Close()
}

func TestLoadConfig(t *testing.T) {
	tmpfile, err := ioutil.TempFile("", "relay-load-config")
	defer os.Remove(tmpfile.Name()) // clean up

	if !assert.NoError(t, err) {
		return
	}

	tmpfile.WriteString(`
[environment "test api key"]
ApiKey = "sdk-98e2b0b4-2688-4a59-9810-1e0e3d798989"

[environment "test api and sdk key"]
ApiKey = "abc"
SdkKey = "sdk-98e2b0b4-2688-4a59-9810-1e0e3d798989"
`)
	tmpfile.Close()

	var c Config
	err = LoadConfigFile(&c, tmpfile.Name())
	if !assert.NoError(t, err) {
		return
	}

	assert.Equal(t, "sdk-98e2b0b4-2688-4a59-9810-1e0e3d798989", c.Environment["test api key"].SdkKey,
		"expected api key to be used as sdk key when api key is set")
	assert.Equal(t, "sdk-98e2b0b4-2688-4a59-9810-1e0e3d798989", c.Environment["test api and sdk key"].SdkKey,
		"expected sdk key to be used as sdk key when both api key and sdk key are set")
}

// jsonFind returns the nested entity at a path in a json obj
func jsonFind(obj map[string]interface{}, paths ...string) interface{} {
	var value interface{} = obj
	for _, p := range paths {
		if v, ok := value.(map[string]interface{}); !ok {
			return nil
		} else {
			value = v[p]
		}
	}
	return value
}
