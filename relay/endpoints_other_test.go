package relay

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"

	ct "github.com/launchdarkly/go-configtypes"
	m "github.com/launchdarkly/go-test-helpers/v2/matchers"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func TestEndpointsJSClientGoals(t *testing.T) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
	fakeGoalsData := []byte(`["got some goals"]`)

	fakeGoalsEndpoint := mux.NewRouter()
	fakeGoalsEndpoint.HandleFunc("/sdk/goals/{envId}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = ioutil.ReadAll(req.Body)
		if mux.Vars(req)["envId"] != string(envID) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakeGoalsData)
	}).Methods("GET")
	fakeServerWithGoalsEndpoint := httptest.NewServer(fakeGoalsEndpoint)
	defer fakeServerWithGoalsEndpoint.Close()

	var config c.Config
	config.Main.BaseURI, _ = ct.NewOptURLAbsoluteFromString(fakeServerWithGoalsEndpoint.URL)
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		url := fmt.Sprintf("http://localhost/sdk/goals/%s", envID)

		t.Run("requests", func(t *testing.T) {
			r := st.BuildRequest("GET", url, nil, nil)
			result, body := st.DoRequest(r, p.relay)
			st.AssertNonStreamingHeaders(t, result.Header)
			if assert.Equal(t, http.StatusOK, result.StatusCode) {
				st.AssertExpectedCORSHeaders(t, result, "GET", "*")
			}
			m.In(t).Assert(body, st.ExpectBody(string(fakeGoalsData)))
		})

		t.Run("options", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.relay, url, "GET")
		})
	})
}
