package sdks

import (
	"errors"
	"net/http"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/config"

	"github.com/gorilla/mux"
)

// Kind represents any of the supported SDK categories that has distinct behavior from the others.
type Kind string

const (
	// Server represents server-side SDKs, which use server-side endpoints and authenticate their requests
	// with an SDK key.
	Server Kind = "server"

	// Mobile represents mobile SDKs, which use mobile endpoints and authenticate their requests with a
	// mobile key.
	Mobile Kind = "mobile"

	// JSClient represents client-side JavaScript-based SDKs, which use client-side endpoints and
	// authenticate their requests insecurely with an environment ID.
	JSClient Kind = "js"
)

var (
	errNoAuthToken    = errors.New("no valid token found")
	errNoEnvID        = errors.New("environment ID not found in URL")
	errUnknownSDKKind = errors.New("unknown SDK kind")
)

// GetCredential attempts to get the appropriate kind of authentication credential for this SDK kind
// from an HTTP request. For Server and Mobile, this uses the Authorization header; for JSClient, it
// is in a path parameter.
func (k Kind) GetCredential(req *http.Request) (config.SDKCredential, error) {
	switch k {
	case Server:
		value, err := fetchAuthToken(req)
		if err == nil {
			// If Authorization header starts with "sdk" treat it as an SDK key
			// Otherwise, treat it as an environment ID
			if strings.HasPrefix(value, "sdk") {
				return config.SDKKey(value), nil
			} else {
				return config.EnvironmentID(value), nil
			}
		}
		return nil, err
	case Mobile:
		value, err := fetchAuthToken(req)
		if err == nil {
			return config.MobileKey(value), nil
		}
		return nil, err
	case JSClient:
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
