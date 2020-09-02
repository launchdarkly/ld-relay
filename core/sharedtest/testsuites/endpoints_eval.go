package testsuites

import (
	"encoding/json"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/core/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"

	"github.com/stretchr/testify/assert"
)

func DoEvalEndpointsTests(t *testing.T, constructor TestConstructor) {
	constructor.RunTest(t, "server-side", DoServerSideEvalRoutesTest)
	constructor.RunTest(t, "mobile", DoMobileEvalRoutesTest)
	constructor.RunTest(t, "JS client", DoJSClientEvalRoutesTest)
}

func DoServerSideEvalRoutesTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvMain
	sdkKey := env.Config.SDKKey
	userJSON := []byte(`{"key":"me"}`)
	expectedServerEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, false, false))
	expectedServerEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, true, false))
	expectedServerEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, true, true))

	specs := []endpointTestParams{
		{"server-side report eval", "REPORT", "/sdk/eval/user", userJSON, sdkKey,
			http.StatusOK, expectedServerEvalBody},
		{"server-side report evalx", "REPORT", "/sdk/evalx/user", userJSON, sdkKey,
			http.StatusOK, expectedServerEvalxBody},
		{"server-side report evalx with reasons", "REPORT", "/sdk/evalx/user?withReasons=true", userJSON, sdkKey,
			http.StatusOK, expectedServerEvalxBodyWithReasons},
	}
	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	DoTest(config, constructor, func(p TestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					result, body := st.DoRequest(s.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						st.AssertNonStreamingHeaders(t, result.Header)
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("unknown SDK key", func(t *testing.T) {
					s1 := s
					s1.credential = st.UndefinedSDKKey
					result, _ := st.DoRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
				})

				for _, user := range allBadUserTestParams {
					u := user
					t.Run(u.name, func(t *testing.T) {
						s1 := s
						s1.data = u.userJSON
						result, _ := st.DoRequest(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}
			})
		}
	})
}

func DoMobileEvalRoutesTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvMobile
	mobileKey := env.Config.MobileKey
	userJSON := []byte(`{"key":"me"}`)
	expectedMobileEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, false, false))
	expectedMobileEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, true, false))
	expectedMobileEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, true, true))

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
	config.Environment = st.MakeEnvConfigs(env)

	DoTest(config, constructor, func(p TestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					result, body := st.DoRequest(s.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
						st.AssertNonStreamingHeaders(t, result.Header)
					}
				})

				t.Run("unknown mobile key", func(t *testing.T) {
					s1 := s
					s1.credential = st.UndefinedMobileKey
					result, _ := st.DoRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
				})

				for _, user := range allBadUserTestParams {
					u := user
					t.Run(u.name, func(t *testing.T) {
						s1 := s
						s1.data = u.userJSON
						result, _ := st.DoRequest(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}
			})
		}
	})
}

func DoJSClientEvalRoutesTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
	user := lduser.NewUser("me")
	userJSON, _ := json.Marshal(user)
	expectedJSEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, false, false))
	expectedJSEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, true, false))
	expectedJSEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, true, true))

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
	config.Environment = st.MakeEnvConfigs(st.EnvClientSide, st.EnvClientSideSecureMode)

	DoTest(config, constructor, func(p TestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				t.Run("success", func(t *testing.T) {
					result, body := st.DoRequest(s.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						st.AssertNonStreamingHeaders(t, result.Header)
						st.AssertExpectedCORSHeaders(t, result, s.method, "*")
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("secure mode - hash matches", func(t *testing.T) {
					s1 := s
					s1.credential = st.EnvClientSideSecureMode.Config.EnvID
					s1.path = st.AddQueryParam(s1.path, "h="+testclient.FakeHashForUser(user))
					result, body := st.DoRequest(s1.request(), p.Handler)

					if assert.Equal(t, s.expectedStatus, result.StatusCode) {
						st.AssertNonStreamingHeaders(t, result.Header)
						st.AssertExpectedCORSHeaders(t, result, s.method, "*")
						if s.bodyMatcher != nil {
							s.bodyMatcher(t, body)
						}
					}
				})

				t.Run("secure mode - hash does not match", func(t *testing.T) {
					s1 := s
					s1.credential = st.EnvClientSideSecureMode.Config.EnvID
					s1.path = st.AddQueryParam(s1.path, "h=incorrect")
					result, _ := st.DoRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				})

				t.Run("secure mode - hash not provided", func(t *testing.T) {
					s1 := s
					s1.credential = st.EnvClientSideSecureMode.Config.EnvID
					result, _ := st.DoRequest(s1.request(), p.Handler)

					assert.Equal(t, http.StatusBadRequest, result.StatusCode)
				})

				t.Run("unknown environment ID", func(t *testing.T) {
					s1 := s
					s1.credential = st.UndefinedEnvID
					result, _ := st.DoRequest(s1.request(), p.Handler)
					assert.Equal(t, http.StatusNotFound, result.StatusCode)
				})

				for _, user := range allBadUserTestParams {
					u := user
					t.Run(u.name, func(t *testing.T) {
						s1 := s
						s1.data = u.userJSON
						result, _ := st.DoRequest(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}

				t.Run("options", func(t *testing.T) {
					st.AssertEndpointSupportsOptionsRequest(t, p.Handler, s.localURL(), s.method)
				})
			})
		}
	})
}
