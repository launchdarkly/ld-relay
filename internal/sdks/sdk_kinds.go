package sdks

import (
	"errors"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"

	"github.com/gorilla/mux"
)

var (
	errNoAuthToken    = errors.New("no valid token found")
	errNoEnvID        = errors.New("environment ID not found in URL")
	errUnknownSDKKind = errors.New("unknown SDK kind")
)

// GetCredential attempts to get the appropriate kind of authentication credential for this SDK kind
// from an HTTP request. For Server and Mobile, this uses the Authorization header; for JSClient, it
// is in a path parameter.
func GetCredential(k basictypes.SDKKind, req *http.Request) (credential.SDKCredential, error) {
	switch k {
	case basictypes.ServerSDK:
		value, err := fetchAuthToken(req)
		if err == nil {
			return config.SDKKey(value), nil
		}
		return nil, err
	case basictypes.MobileSDK:
		value, err := fetchAuthToken(req)
		if err == nil {
			return config.MobileKey(value), nil
		}
		return nil, err
	case basictypes.JSClientSDK:
		value := mux.Vars(req)["envId"]
		if value == "" {
			return nil, errNoEnvID
		}
		return config.EnvironmentID(value), nil
	}
	return nil, errUnknownSDKKind
}

// FetchClientSideAuthToken extracts a client-side auth token from the request.
// It first checks the Authorization header, then falls back to the "auth" query parameter.
func FetchClientSideAuthToken(req *http.Request) (string, error) {
	token, err := fetchAuthToken(req)
	if err == nil {
		return token, nil
	}
	if authParam := req.URL.Query().Get("auth"); authParam != "" {
		return authParam, nil
	}
	return "", errNoAuthToken
}

func fetchAuthToken(req *http.Request) (string, error) {
	authHdr := req.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "api_key ") {
		authHdr = strings.TrimSpace(strings.TrimPrefix(authHdr, "api_key "))
	}
	if authHdr == "" || strings.Contains(authHdr, " ") {
		return "", errNoAuthToken
	}
	return authHdr, nil
}
