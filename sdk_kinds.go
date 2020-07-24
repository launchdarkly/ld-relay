package relay

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/launchdarkly/ld-relay/v6/config"
)

type sdkKind string

const (
	serverSdk   sdkKind = "server"
	jsClientSdk sdkKind = "js"
	mobileSdk   sdkKind = "mobile"
)

var (
	errNoAuthToken    = errors.New("no valid token found")
	errNoEnvID        = errors.New("environment ID not found in URL")
	errUnknownSDKKind = errors.New("unknown SDK kind")
)

func (s sdkKind) getSDKCredential(req *http.Request) (config.SDKCredential, error) {
	switch s {
	case serverSdk:
		value, err := fetchAuthToken(req)
		if err == nil {
			return config.SDKKey(value), nil
		}
		return nil, err
	case mobileSdk:
		value, err := fetchAuthToken(req)
		if err == nil {
			return config.MobileKey(value), nil
		}
		return nil, err
	case jsClientSdk:
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
