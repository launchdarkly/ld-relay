package sdks

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"

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

func TestFetchClientSideAuthToken(t *testing.T) {
	t.Run("from Authorization header", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://fake", nil)
		req.Header.Set("Authorization", "my-token")
		token, err := FetchClientSideAuthToken(req)
		assert.NoError(t, err)
		assert.Equal(t, "my-token", token)
	})

	t.Run("from Authorization header with api_key prefix", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://fake", nil)
		req.Header.Set("Authorization", "api_key my-token")
		token, err := FetchClientSideAuthToken(req)
		assert.NoError(t, err)
		assert.Equal(t, "my-token", token)
	})

	t.Run("from auth query param when no header", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://fake?auth=query-token", nil)
		token, err := FetchClientSideAuthToken(req)
		assert.NoError(t, err)
		assert.Equal(t, "query-token", token)
	})

	t.Run("header takes precedence over query param", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://fake?auth=query-token", nil)
		req.Header.Set("Authorization", "header-token")
		token, err := FetchClientSideAuthToken(req)
		assert.NoError(t, err)
		assert.Equal(t, "header-token", token)
	})

	t.Run("error when neither header nor query param", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://fake", nil)
		_, err := FetchClientSideAuthToken(req)
		assert.Error(t, err)
	})
}
