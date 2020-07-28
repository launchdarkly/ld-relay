package relay

import (
	"encoding/base64"
	"net/http"
	"strings"

	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/testhelpers"

	"github.com/launchdarkly/ld-relay/v6/config"
	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/logging"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
)

// Environment that is passed to test code with relayTest.
type relayTestParams struct {
	relay *Relay
}

// Runs some code against a new Relay instance that is set up with the specified configuration.
func relayTest(config c.Config, action func(relayTestParams)) {
	p := relayTestParams{}

	createDummyClient := func(sdkKey c.SDKKey, sdkConfig ld.Config) (sdks.LDClientContext, error) {
		store, _ := sdkConfig.DataStore.(*store.SSERelayDataStoreAdapter).CreateDataStore(
			testhelpers.NewSimpleClientContext(string(sdkKey)), nil)
		err := store.Init(allData)
		if err != nil {
			panic(err)
		}
		return &fakeLDClient{true}, nil
	}

	relay, _ := newRelayInternal(config, logging.MakeDefaultLoggers(), createDummyClient)
	defer relay.Close()
	p.relay = relay

	action(p)
}

// Test parameters for an endpoint that we want to test. The "data" parameter is used as the request body if
// the method is GET, and can also be included in base64 in the URL by putting "$DATA" in the URL path. Also,
// if the credential is an environment ID, it is substituted for "$ENV" in the URL path.
type endpointTestParams struct {
	name           string
	method         string
	path           string
	data           []byte
	credential     config.SDKCredential
	expectedStatus int
	bodyMatcher    bodyMatcher
}

func (e endpointTestParams) request() *http.Request {
	return buildRequest(e.method, e.localURL(), e.data, e.header())
}

func (e endpointTestParams) header() http.Header {
	h := make(http.Header)
	if e.credential != nil && e.credential.GetAuthorizationHeaderValue() != "" {
		h.Set("Authorization", e.credential.GetAuthorizationHeaderValue())
	}
	if e.data != nil && e.method != "GET" {
		h.Set("Content-Type", "application/json")
	}
	return h
}

func (e endpointTestParams) localURL() string {
	p := e.path
	if strings.Contains(p, "$ENV") {
		if env, ok := e.credential.(config.EnvironmentID); ok {
			p = strings.ReplaceAll(p, "$ENV", string(env))
		} else {
			panic("test endpoint URL had $ENV but did not specify an environment ID")
		}
	}
	if strings.Contains(p, "$USER") {
		if e.data != nil {
			p = strings.ReplaceAll(p, "$USER", base64.StdEncoding.EncodeToString(e.data))
		} else {
			panic("test endpoint URL had $USER but did not specify any data")
		}
	}
	if strings.Contains(p, "$DATA") {
		if e.data != nil {
			p = strings.ReplaceAll(p, "$DATA", base64.StdEncoding.EncodeToString(e.data))
		} else {
			panic("test endpoint URL had $DATA but did not specify any data")
		}
	}
	if strings.Contains(p, "$") {
		panic("test endpoint URL had unrecognized format")
	}
	return "http://localhost" + p
}

// Test parameters for user data that should be rejected as invalid.
type badUserTestParams struct {
	name     string
	userJSON []byte
}

func (u badUserTestParams) base64User() string {
	return base64.StdEncoding.EncodeToString([]byte(u.userJSON))
}

var allBadUserTestParams = []badUserTestParams{
	{"invalid user JSON", []byte(`{"key":"incomplete`)},
	{"missing user key", []byte(`{"name":"Keyless Joe"}`)},
}
