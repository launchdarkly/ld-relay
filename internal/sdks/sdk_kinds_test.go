package sdks

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func TestGetCredential(t *testing.T) {
	for _, authHeaderValue := range []string{"abc", "api_key abc"} {
		reqWithAuth, _ := http.NewRequest("GET", "http://fake", nil)
		reqWithAuth.Header.Set("Authorization", authHeaderValue)

		c1, err := GetCredential(basictypes.ServerSDK, reqWithAuth)
		assert.NoError(t, err)
		assert.Equal(t, config.SDKKey("abc"), c1)

		c2, err := GetCredential(basictypes.MobileSDK, reqWithAuth)
		assert.NoError(t, err)
		assert.Equal(t, config.MobileKey("abc"), c2)

		c3, err := GetCredential(basictypes.JSClientSDK, reqWithAuth)
		assert.Error(t, err)
		assert.Nil(t, c3)
	}

	reqWithURLParam, _ := http.NewRequest("GET", "http://fake/path/xyz", nil)
	router := mux.NewRouter()
	router.HandleFunc("/path/{envId:[a-z]+}", func(w http.ResponseWriter, r *http.Request) {
		c1, err := GetCredential(basictypes.ServerSDK, r)
		assert.Error(t, err)
		assert.Nil(t, c1)

		c2, err := GetCredential(basictypes.MobileSDK, r)
		assert.Error(t, err)
		assert.Nil(t, c2)

		c3, err := GetCredential(basictypes.JSClientSDK, r)
		assert.NoError(t, err)
		assert.Equal(t, config.EnvironmentID("xyz"), c3)
	})
	router.ServeHTTP(httptest.NewRecorder(), reqWithURLParam)

	var nilKind basictypes.SDKKind
	r, _ := http.NewRequest("GET", "", nil)
	c, err := GetCredential(nilKind, r)
	assert.Error(t, err)
	assert.Nil(t, c)
}
