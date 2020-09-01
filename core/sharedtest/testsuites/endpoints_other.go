package testsuites

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/core/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"

	ct "github.com/launchdarkly/go-configtypes"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func DoJSClientGoalsEndpointTest(t *testing.T, constructor TestConstructor) {
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

	DoTest(config, constructor, func(p TestParams) {
		url := fmt.Sprintf("http://localhost/sdk/goals/%s", envID)

		t.Run("requests", func(t *testing.T) {
			r := st.BuildRequest("GET", url, nil, nil)
			result, body := st.DoRequest(r, p.Handler)
			st.AssertNonStreamingHeaders(t, result.Header)
			if assert.Equal(t, http.StatusOK, result.StatusCode) {
				st.AssertExpectedCORSHeaders(t, result, "GET", "*")
			}
			st.ExpectBody(string(fakeGoalsData))(t, body)
		})

		t.Run("options", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.Handler, url, "GET")
		})
	})
}
