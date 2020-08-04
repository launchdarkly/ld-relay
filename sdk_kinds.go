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
			return nil, errors.New("environment ID not found in URL")
		}
		return config.EnvironmentID(value), nil
	}
	return nil, errors.New("unknown SDK kind")
}

func fetchAuthToken(req *http.Request) (string, error) {
	authHdr := req.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "api_key ") {
		authHdr = strings.TrimSpace(strings.TrimPrefix(authHdr, "api_key "))
	}
	if authHdr == "" || strings.Contains(authHdr, " ") {
		return "", errors.New("no valid token found")
	}
	return authHdr, nil
}
