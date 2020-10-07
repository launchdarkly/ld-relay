package testsuites

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/browser"
	"github.com/launchdarkly/ld-relay/v6/internal/core/internal/events"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	ct "github.com/launchdarkly/go-configtypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func DoEventProxyTests(t *testing.T, constructor TestConstructor) {
	constructor.RunTest(t, "server-side", DoServerSideEventProxyTest)
	constructor.RunTest(t, "mobile", DoMobileEventProxyTest)
	constructor.RunTest(t, "JS client", DoJSClientEventProxyTest)
}

type publishedEvent struct {
	url     string
	data    []byte
	authKey string
}

type relayEventsTestParams struct {
	TestParams
	publishedEvents chan publishedEvent
}

func (p relayEventsTestParams) requirePublishedEvent(t *testing.T, data []byte) publishedEvent {
	timeout := time.After(time.Second * 3)
	select {
	case event := <-p.publishedEvents:
		assert.JSONEq(t, string(data), string(event.data))
		return event
	case <-timeout:
		require.Fail(t, "did not get event within 3 seconds")
		return publishedEvent{} // won't get here
	}
}

// Runs some code against a new Relay instance that is set up with the specified configuration, along with a
// test server to receie any events that are proxied by Relay.
func relayEventsTest(t *testing.T, config c.Config, constructor TestConstructor, action func(relayEventsTestParams)) {
	eventsCh := make(chan publishedEvent, 10)

	eventsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, _ := ioutil.ReadAll(req.Body)
		eventsCh <- publishedEvent{url: req.URL.String(), data: data, authKey: req.Header.Get("Authorization")}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer eventsServer.Close()

	config.Main.DisableInternalUsageMetrics = true // so the metrics event exporter doesn't produce unexpected events
	config.Events.SendEvents = true
	config.Events.EventsURI, _ = ct.NewOptURLAbsoluteFromString(eventsServer.URL)
	config.Events.FlushInterval = ct.NewOptDuration(time.Second)

	DoTest(t, config, constructor, func(pBase TestParams) {
		p := relayEventsTestParams{
			TestParams:      pBase,
			publishedEvents: eventsCh,
		}
		action(p)
	})
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

func DoServerSideEventProxyTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvMain
	sdkKey := env.Config.SDKKey
	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)
	body := makeTestFeatureEventPayload("me")

	makeRequest := func(authKey c.SDKKey) *http.Request {
		header := make(http.Header)
		header.Set("Authorization", string(authKey))
		header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
		return st.BuildRequest("POST", "http://localhost/bulk", body, header)
	}

	relayEventsTest(t, config, constructor, func(p relayEventsTestParams) {
		t.Run("bulk post", func(t *testing.T) {
			r := makeRequest(sdkKey)
			result, _ := st.DoRequest(r, p.Handler)

			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := p.requirePublishedEvent(t, body)
				assert.Equal(t, "/bulk", event.url)
				assert.Equal(t, string(sdkKey), event.authKey)
			}
		})

		t.Run("unknown SDK key", func(t *testing.T) {
			r := makeRequest(st.UndefinedSDKKey)
			result, _ := st.DoRequest(r, p.Handler)

			assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
		})

		t.Run("diagnostics forwarding", func(t *testing.T) {
			eventData := []byte(`{"kind":"diagnostic"}`)
			header := make(http.Header)
			header.Set("Content-Type", "application/json")
			header.Set("Authorization", string(sdkKey))
			r := st.BuildRequest("POST", "http://localhost/diagnostic", eventData, header)
			result, _ := st.DoRequest(r, p.Handler)

			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := p.requirePublishedEvent(t, eventData)
				assert.Equal(t, "/diagnostic", event.url)
				assert.Equal(t, string(sdkKey), event.authKey)
			}
		})
	})

	t.Run("events disabled", func(t *testing.T) {
		DoTest(t, config, constructor, func(p TestParams) {
			r := makeRequest(sdkKey)
			result, _ := st.DoRequest(r, p.Handler)
			assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode)
		})
	})
}

func DoMobileEventProxyTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvMobile
	mobileKey := env.Config.MobileKey
	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	bulkEndpoints := []string{"/mobile/events", "/mobile/events/bulk"}

	makeBody := func(i int) []byte {
		return makeTestFeatureEventPayload(fmt.Sprintf("me%d", i))
	}

	makeRequest := func(path string, body []byte, authKey c.MobileKey) *http.Request {
		url := "http://localhost" + path
		header := make(http.Header)
		header.Set("Authorization", string(authKey))
		header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
		return st.BuildRequest("POST", url, body, header)
	}

	relayEventsTest(t, config, constructor, func(p relayEventsTestParams) {
		for i, path := range bulkEndpoints {
			body := makeBody(i)

			t.Run("bulk post "+path, func(t *testing.T) {
				r := makeRequest(path, body, mobileKey)
				result, _ := st.DoRequest(r, p.Handler)

				if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
					event := p.requirePublishedEvent(t, body)
					assert.Equal(t, "/mobile", event.url)
					assert.Equal(t, string(mobileKey), event.authKey)
				}
			})

			t.Run("unknown SDK key", func(t *testing.T) {
				r := makeRequest(path, body, st.UndefinedMobileKey)
				result, _ := st.DoRequest(r, p.Handler)

				assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
			})
		}

		t.Run("diagnostics forwarding", func(t *testing.T) {
			eventData := []byte(`{"kind":"diagnostic"}`)
			header := make(http.Header)
			header.Set("Authorization", string(mobileKey))
			r := st.BuildRequest("POST", "http://localhost/mobile/events/diagnostic", eventData, header)
			result, _ := st.DoRequest(r, p.Handler)

			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := p.requirePublishedEvent(t, eventData)
				assert.Equal(t, "/mobile/events/diagnostic", event.url)
				assert.Equal(t, string(mobileKey), event.authKey)
			}
		})
	})

	t.Run("events disabled", func(t *testing.T) {
		DoTest(t, config, constructor, func(p TestParams) {
			for i, path := range bulkEndpoints {
				t.Run("bulk post "+path, func(t *testing.T) {
					r := makeRequest(path, makeBody(i), mobileKey)
					result, _ := st.DoRequest(r, p.Handler)
					assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode)
				})
			}
		})
	})
}

func DoJSClientEventProxyTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
	eventData := makeTestFeatureEventPayload("me")

	specs := []endpointTestParams{
		{"post events", "POST", "/events/bulk/$ENV", eventData, envID, http.StatusAccepted, nil},
		{"get events image", "GET", "/a/$ENV.gif?d=$DATA", eventData, envID, http.StatusOK,
			st.ExpectBody(string(browser.Transparent1PixelImageData))},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	relayEventsTest(t, config, constructor, func(p relayEventsTestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					r := s.request()
					if s.method != "GET" {
						r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
					}
					result, body := st.DoRequest(r, p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						st.AssertNonStreamingHeaders(t, result.Header)
						st.AssertExpectedCORSHeaders(t, result, s.method, "*")

						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}

						event := p.requirePublishedEvent(t, eventData)
						assert.Equal(t, fmt.Sprintf("/events/bulk/%s", envID), event.url)
						assert.Equal(t, "", event.authKey)
					}
				})

				t.Run("unknown environment ID", func(t *testing.T) {
					s1 := s
					s1.credential = st.UndefinedEnvID
					r := s1.request()
					if s1.method != "GET" {
						r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
					}
					result, _ := st.DoRequest(r, p.Handler)

					assert.Equal(t, http.StatusNotFound, result.StatusCode)
				})

				t.Run("options", func(t *testing.T) {
					st.AssertEndpointSupportsOptionsRequest(t, p.Handler, s.localURL(), s.method)
				})
			})
		}

		t.Run("diagnostics forwarding", func(t *testing.T) {
			expectedPath := fmt.Sprintf("/events/diagnostic/%s", envID)
			eventData := []byte(`{"kind":"diagnostic"}`)
			header := make(http.Header)
			header.Set("Content-Type", "application/json")
			r := st.BuildRequest("POST", "http://localhost"+expectedPath, eventData, header)
			result, _ := st.DoRequest(r, p.Handler)

			if assert.Equal(t, http.StatusAccepted, result.StatusCode) {
				event := p.requirePublishedEvent(t, eventData)
				assert.Equal(t, expectedPath, event.url)
				assert.Equal(t, "", event.authKey)
			}
		})
	})

	t.Run("events disabled", func(t *testing.T) {
		DoTest(t, config, constructor, func(p TestParams) {
			for _, spec := range specs {
				s := spec
				t.Run(s.name, func(t *testing.T) {
					r := s.request()
					if s.method != "GET" {
						r.Header.Set(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
					}
					result, _ := st.DoRequest(r, p.Handler)
					assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode)
				})
			}
		})
	})
}
