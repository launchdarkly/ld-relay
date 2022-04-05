package sharedtest

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
)

// These methods should be used by all test code that makes HTTP requests to Relay endpoints.
// We are deliberately not sharing the same URL path strings between this test code and the non-test code
// that configures Relay's endpoint routing, so that if we accidentally change the routing, the tests
// will fail, rather than succeed based on incorrect paths.

// SimpleUserJSON is a basic user.
const SimpleUserJSON = `{"key":"userkey"}`

// ToBase64 is a shortcut for base64 encoding.
func ToBase64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// SDKRequestVariant represents distinctions between endpoints that are not described in full by
// basictypes.SDKKind or basictypes.StreamKind.
type SDKRequestVariant int

const (
	// Evalx indicates that a client-side request wants the "evalx" format rather than the older
	// value-only format.
	Evalx SDKRequestVariant = 2

	// ReportMode means a client-side request should be done with REPORT rather than GET.
	ReportMode SDKRequestVariant = 4
)

// MakeSDKStreamEndpointRequest creates a request to one of the streaming SDK endpoints.
func MakeSDKStreamEndpointRequest(
	baseURL string,
	kind basictypes.StreamKind,
	testEnv TestEnv,
	userJSON string,
	variant SDKRequestVariant,
) *http.Request {
	switch {
	case kind == basictypes.ServerSideStream:
		return BuildRequestWithAuth("GET", fmt.Sprintf("%s/all", baseURL), testEnv.Config.SDKKey, nil)
	case kind == basictypes.ServerSideFlagsOnlyStream:
		return BuildRequestWithAuth("GET", fmt.Sprintf("%s/flags", baseURL), testEnv.Config.SDKKey, nil)
	case kind == basictypes.MobilePingStream && (variant&ReportMode == 0):
		return BuildRequestWithAuth("GET",
			fmt.Sprintf("%s/m%s/%s", baseURL, evalOrEvalx(variant), ToBase64(userJSON)),
			testEnv.Config.MobileKey, nil)
	case kind == basictypes.MobilePingStream && (variant&ReportMode != 0):
		return BuildRequestWithAuth("REPORT",
			fmt.Sprintf("%s/m%s", baseURL, evalOrEvalx(variant)),
			testEnv.Config.MobileKey, []byte(ToBase64(userJSON)))
	case kind == basictypes.JSClientPingStream && (variant&ReportMode == 0):
		return BuildRequest("GET",
			fmt.Sprintf("%s/%s/%s/%s", baseURL, evalOrEvalx(variant), testEnv.Config.EnvID, ToBase64(userJSON)),
			nil, nil)
	case kind == basictypes.JSClientPingStream && (variant&ReportMode != 0):
		return BuildRequest("REPORT",
			fmt.Sprintf("%s/%s/%s", baseURL, evalOrEvalx(variant), testEnv.Config.EnvID),
			[]byte(ToBase64(userJSON)), nil)
	default:
		panic("invalid StreamKind value")
	}
}

func MakeSDKEvalEndpointRequest(baseURL string, kind basictypes.SDKKind, testEnv TestEnv, userJSON string, variant SDKRequestVariant) *http.Request {
	switch {
	case kind == basictypes.MobileSDK && (variant&ReportMode == 0):
		return BuildRequestWithAuth("GET",
			fmt.Sprintf("%s/msdk/%s/users/%s", baseURL, evalOrEvalx(variant), ToBase64(userJSON)),
			testEnv.Config.MobileKey, nil)
	case kind == basictypes.MobileSDK && (variant&ReportMode != 0):
		return BuildRequestWithAuth("REPORT",
			fmt.Sprintf("%s/msdk/%s/user", baseURL, evalOrEvalx(variant)),
			testEnv.Config.MobileKey, []byte(ToBase64(userJSON)))
	case kind == basictypes.JSClientSDK && (variant&ReportMode == 0):
		return BuildRequestWithAuth("GET",
			fmt.Sprintf("%s/sdk/%s/%s/users/%s", baseURL, evalOrEvalx(variant), testEnv.Config.EnvID, ToBase64(userJSON)),
			nil, nil)
	case kind == basictypes.JSClientSDK && (variant&ReportMode != 0):
		return BuildRequestWithAuth("REPORT",
			fmt.Sprintf("%s/sdk/%s/%s/user", baseURL, evalOrEvalx(variant), testEnv.Config.EnvID),
			nil, []byte(ToBase64(userJSON)))
	default:
		panic("invalid SDKKind value")
	}
}

func evalOrEvalx(variant SDKRequestVariant) string {
	if variant&Evalx == 0 {
		return "eval"
	}
	return "evalx"
}
