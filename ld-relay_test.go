package main

import (
	"github.com/gorilla/mux"
	"github.com/streamrail/concurrent-map"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"bytes"
	ld "gopkg.in/launchdarkly/go-client.v2"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
)

type FakeLDClient struct {
	mock.Mock
}

func (client *FakeLDClient) AllFlags(user ld.User) map[string]interface{} {
	flags := make(map[string]interface{})
	flags["some-flag-key"] = true
	flags["another-flag-key"] = 3
	return flags
}

// Returns a mobile key matching the UUID header pattern
func mobKey() string {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func envId() string {
	return "58ffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

func TestGetFlagEvalSucceeds(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/{user}", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", mobKey())
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlags(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/" + user())

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestReportFlagEvalSucceeds(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		jsonStr := []byte(`{"user":"key"}`)
		r, _ = http.NewRequest("REPORT", "", bytes.NewBuffer(jsonStr))
		r.Header.Set("Authorization", mobKey())
		r.Header.Set("Content-Type", "application/json")
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlags(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestFlagEvalFailsOnInvalidAuthKey(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		jsonStr := []byte(`{"user":"key"}`)
		r, _ = http.NewRequest("REPORT", "", bytes.NewBuffer(jsonStr))
		r.Header.Set("Authorization", "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee")
		r.Header.Set("Content-Type", "application/json")
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlags(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL)

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		jsonStr := []byte(`{"user":"key"}1`)
		r, _ = http.NewRequest("REPORT", "", bytes.NewBuffer(jsonStr))
		r.Header.Set("Authorization", mobKey())
		r.Header.Set("Content-Type", "application/json")
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlags(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestClientSideFlagEvalSucceeds(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/{envId}/{user}", func(w http.ResponseWriter, r *http.Request) {
		clients := cmap.New()
		clients.Set(envId(), &FakeLDClient{})
		r.Header.Set("Content-Type", "application/json")
		evaluateAllFeatureFlagsForClientSide(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/" + envId() + "/" + user())

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestClientSideFlagEvalFailsOnInvalidEnvId(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/{envId}/{user}", func(w http.ResponseWriter, r *http.Request) {
		clients := cmap.New()
		clients.Set(envId(), &FakeLDClient{})
		r.Header.Set("Content-Type", "application/json")
		evaluateAllFeatureFlagsForClientSide(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/blah/" + user())

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
