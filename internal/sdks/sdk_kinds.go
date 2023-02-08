package sdks

import (
	"errors"
	"net/http"
	"strings"

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
func GetCredential(k basictypes.SDKKind, req *http.Request) (config.SDKCredential, error) {
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
