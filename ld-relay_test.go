package main

import (
	"fmt"
	"github.com/streamrail/concurrent-map"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"bytes"
	"github.com/gorilla/mux"
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

func TestMobileGetEvalSucceeds(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/msdk/eval/user/{user}", func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", mobKey())
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlagsForMobile(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/msdk/eval/user/eyJrZXkiOiJ0ZXN0In0=")

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestMobileReportEvalSucceeds(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/msdk/eval/users", func(w http.ResponseWriter, r *http.Request) {
		jsonStr := []byte(`{"user":"key"}`)
		r, _ = http.NewRequest("REPORT", "", bytes.NewBuffer(jsonStr))
		r.Header.Set("Authorization", mobKey())
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlagsForMobile(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/msdk/eval/users")

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)

	assert.Equal(t, `{"another-flag-key":3,"some-flag-key":true}`, string(b))
}

func TestMobileEvalFailsOnInvalidMobileKey(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/msdk/eval/users", func(w http.ResponseWriter, r *http.Request) {
		jsonStr := []byte(`{"user":"key"}`)
		r, _ = http.NewRequest("REPORT", "", bytes.NewBuffer(jsonStr))
		r.Header.Set("Authorization", "mob-eeeeeeee-eeee-4eee-aeee-eeeeeeeeeeee")
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlagsForMobile(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/msdk/eval/users")

	assert.Equal(t, 401, resp.StatusCode)
}

func TestMobileFlagEvalFailsOnInvalidUserJson(t *testing.T) {
	r := mux.NewRouter()
	r.HandleFunc("/msdk/eval/users", func(w http.ResponseWriter, r *http.Request) {
		jsonStr := []byte(`{"user":"key"}1`)
		r, _ = http.NewRequest("REPORT", "", bytes.NewBuffer(jsonStr))
		r.Header.Set("Authorization", mobKey())
		clients := cmap.New()
		clients.Set(mobKey(), &FakeLDClient{})
		evaluateAllFeatureFlagsForMobile(w, r, clients)
	})
	server := httptest.NewServer(r)

	resp, _ := http.Get(server.URL + "/msdk/eval/users")

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
