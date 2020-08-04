package relay

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"

	c "github.com/launchdarkly/ld-relay/v6/config"
)

func DoEvalEndpointsTests(t *testing.T, constructor TestConstructor) {
	constructor.RunTest(t, "server-side", DoServerSideEvalRoutesTest)
	constructor.RunTest(t, "mobile", DoMobileEvalRoutesTest)
	constructor.RunTest(t, "JS client", DoJSClientEvalRoutesTest)
}

func DoServerSideEvalRoutesTest(t *testing.T, constructor TestConstructor) {
	env := testEnvMain
	sdkKey := env.config.SDKKey
	userJSON := []byte(`{"key":"me"}`)
	expectedMobileEvalBody := expectJSONBody(makeEvalBody(allFlags, false, false))
	expectedMobileEvalxBody := expectJSONBody(makeEvalBody(allFlags, true, false))
	expectedMobileEvalxBodyWithReasons := expectJSONBody(makeEvalBody(allFlags, true, true))

	specs := []endpointTestParams{
		{"server-side report eval", "REPORT", "/sdk/eval/user", userJSON, sdkKey,
			http.StatusOK, expectedMobileEvalBody},
		{"server-side report evalx", "REPORT", "/sdk/evalx/user", userJSON, sdkKey,
			http.StatusOK, expectedMobileEvalxBody},
		{"server-side report evalx with reasons", "REPORT", "/sdk/evalx/user?withReasons=true", userJSON, sdkKey,
			http.StatusOK, expectedMobileEvalxBodyWithReasons},
	}
	var config c.Config
	config.Environment = makeEnvConfigs(env)

	DoTest(config, constructor, func(p TestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					result, body := doRequest(s.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						assertNonStreamingHeaders(t, result.Header)
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("unknown SDK key", func(t *testing.T) {
					s1 := s
					s1.credential = undefinedSDKKey
					result, _ := doRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
				})

				for _, u := range allBadUserTestParams {
					t.Run(u.name, func(t *testing.T) {
						s1 := s
						s1.data = u.userJSON
						result, _ := doRequest(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}
			})
		}
	})
}

func DoMobileEvalRoutesTest(t *testing.T, constructor TestConstructor) {
	env := testEnvMobile
	mobileKey := env.config.MobileKey
	userJSON := []byte(`{"key":"me"}`)
	expectedMobileEvalBody := expectJSONBody(makeEvalBody(allFlags, false, false))
	expectedMobileEvalxBody := expectJSONBody(makeEvalBody(allFlags, true, false))
	expectedMobileEvalxBodyWithReasons := expectJSONBody(makeEvalBody(allFlags, true, true))

	specs := []endpointTestParams{
		{"mobile report eval", "REPORT", "/msdk/eval/user", userJSON, mobileKey,
			http.StatusOK, expectedMobileEvalBody},
		{"mobile report evalx", "REPORT", "/msdk/evalx/user", userJSON, mobileKey,
			http.StatusOK, expectedMobileEvalxBody},
		{"mobile report evalx with reasons", "REPORT", "/msdk/evalx/user?withReasons=true", userJSON, mobileKey,
			http.StatusOK, expectedMobileEvalxBodyWithReasons},
		{"mobile get eval", "GET", "/msdk/eval/users/$USER", userJSON, mobileKey, http.StatusOK, nil},
		{"mobile get evalx", "GET", "/msdk/evalx/users/$USER", userJSON, mobileKey, http.StatusOK, nil},
	}

	var config c.Config
	config.Environment = makeEnvConfigs(env)

	DoTest(config, constructor, func(p TestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					result, body := doRequest(s.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
						assertNonStreamingHeaders(t, result.Header)
					}
				})

				t.Run("unknown mobile key", func(t *testing.T) {
					s1 := s
					s1.credential = undefinedMobileKey
					result, _ := doRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
				})

				for _, u := range allBadUserTestParams {
					t.Run(u.name, func(t *testing.T) {
						s1 := s
						s1.data = u.userJSON
						result, _ := doRequest(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}
			})
		}
	})
}

func DoJSClientEvalRoutesTest(t *testing.T, constructor TestConstructor) {
	env := testEnvClientSide
	envID := env.config.EnvID
	user := lduser.NewUser("me")
	userJSON, _ := json.Marshal(user)
	expectedJSEvalBody := expectJSONBody(makeEvalBody(clientSideFlags, false, false))
	expectedJSEvalxBody := expectJSONBody(makeEvalBody(clientSideFlags, true, false))
	expectedJSEvalxBodyWithReasons := expectJSONBody(makeEvalBody(clientSideFlags, true, true))

	specs := []endpointTestParams{
		{"report eval", "REPORT", "/sdk/eval/$ENV/user", userJSON, envID, http.StatusOK, expectedJSEvalBody},
		{"report evalx", "REPORT", "/sdk/evalx/$ENV/user", userJSON, envID, http.StatusOK, expectedJSEvalxBody},
		{"report evalx with reasons", "REPORT", "/sdk/evalx/$ENV/user?withReasons=true", userJSON, envID,
			http.StatusOK, expectedJSEvalxBodyWithReasons},
		{"get eval", "GET", "/sdk/eval/$ENV/users/$USER", userJSON, envID, http.StatusOK, expectedJSEvalBody},
		{"get evalx", "GET", "/sdk/evalx/$ENV/users/$USER", userJSON, envID, http.StatusOK, expectedJSEvalxBody},
		{"get evalx with reasons", "GET", "/sdk/evalx/$ENV/users/$USER?withReasons=true", userJSON, envID,
			http.StatusOK, expectedJSEvalxBodyWithReasons},
	}

	var config c.Config
	config.Environment = makeEnvConfigs(testEnvClientSide, testEnvClientSideSecureMode)

	DoTest(config, constructor, func(p TestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					result, body := doRequest(s.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						assertNonStreamingHeaders(t, result.Header)
						assertExpectedCORSHeaders(t, result, s.method, "*")
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("secure mode - hash matches", func(t *testing.T) {
					s1 := s
					s1.credential = testEnvClientSideSecureMode.config.EnvID
					s1.path = addQueryParam(s1.path, "h="+fakeHashForUser(user))
					result, body := doRequest(s1.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						assertNonStreamingHeaders(t, result.Header)
						assertExpectedCORSHeaders(t, result, s.method, "*")
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("secure mode - hash does not match", func(t *testing.T) {
					s1 := s
					s1.credential = testEnvClientSideSecureMode.config.EnvID
					s1.path = addQueryParam(s1.path, "h=incorrect")
					result, _ := doRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				})

				t.Run("secure mode - hash not provided", func(t *testing.T) {
					s1 := s
					s1.credential = testEnvClientSideSecureMode.config.EnvID
					result, _ := doRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				})

				t.Run("unknown environment ID", func(t *testing.T) {
					s1 := s
					s1.credential = undefinedEnvID
					result, _ := doRequest(s1.request(), p.Handler)
					assert.Equal(t, http.StatusNotFound, result.StatusCode)
				})

				for _, u := range allBadUserTestParams {
					t.Run(u.name, func(t *testing.T) {
						s1 := s
						s1.data = u.userJSON
						result, _ := doRequest(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}

				t.Run("options", func(t *testing.T) {
					assertEndpointSupportsOptionsRequest(t, p.Handler, s.localURL(), s.method)
				})
			})
		}
	})
}
