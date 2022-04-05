package relay

import (
	"encoding/json"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/lduser"
	"github.com/launchdarkly/go-test-helpers/v2/jsonhelpers"
	m "github.com/launchdarkly/go-test-helpers/v2/matchers"

	"github.com/stretchr/testify/assert"
)

// These user and context representations are designed to be equivalent in terms of the test flags
// we are evaluating, which reference a user key. This just proves that the evaluation endpoints
// are able to unmarshal either old-style user JSON or new context JSON, and that the resulting
// context is being passed to a version of the evaluation engine that understands contexts.
var basicUserJSON = jsonhelpers.ToJSON(st.BasicUserForTestFlags)
var basicContextJSON = jsonhelpers.ToJSON(
	ldcontext.NewMulti(ldcontext.NewWithKind("other", "wrongkey"), st.BasicUserForTestFlags))

func TestEndpointsEvalServerSide(t *testing.T) {
	env := st.EnvMain
	sdkKey := env.Config.SDKKey
	expectedServerEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, false, false))
	expectedServerEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, true, false))
	expectedServerEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, true, true))

	specs := []endpointMultiTestParams{
		{"server-side report eval", "REPORT", "/sdk/eval/user", sdkKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedServerEvalBody)},
		{"server-side report evalx", "REPORT", "/sdk/evalx/user", sdkKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedServerEvalxBody)},
		{"server-side report evalx with reasons", "REPORT", "/sdk/evalx/user?withReasons=true", sdkKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedServerEvalxBodyWithReasons)},
	}
	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				for _, req := range s.requests {
					r := req
					t.Run(r.name, func(t *testing.T) {
						t.Run("success", func(t *testing.T) {
							result, body := st.DoRequest(s.request(r), p.relay)

							if assert.Equal(t, r.expectedStatus, result.StatusCode) {
								st.AssertNonStreamingHeaders(t, result.Header)
								m.In(t).Assert(body, r.bodyMatcher)
							}
						})

						t.Run("unknown SDK key", func(t *testing.T) {
							s1 := s
							s1.credential = st.UndefinedSDKKey
							result, _ := st.DoRequest(s1.request(r), p.relay)

							assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
						})

						for _, user := range allBadUserTestParams {
							u := user
							t.Run(u.name, func(t *testing.T) {
								r1 := r
								r1.data = u.userJSON
								result, _ := st.DoRequest(s.request(r1), p.relay)

								assert.Equal(t, http.StatusBadRequest, result.StatusCode)
							})
						}
					})
				}
			})
		}
	})
}

func TestEndpointsEvalMobile(t *testing.T) {
	env := st.EnvMobile
	mobileKey := env.Config.MobileKey
	expectedMobileEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, false, false))
	expectedMobileEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, true, false))
	expectedMobileEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, true, true))

	specs := []endpointMultiTestParams{
		{"mobile report eval", "REPORT", "/msdk/eval/user", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalBody)},
		{"mobile report evalx", "REPORT", "/msdk/evalx/user", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBody)},
		{"mobile report evalx with reasons", "REPORT", "/msdk/evalx/user?withReasons=true", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBodyWithReasons)},
		{"mobile get eval", "GET", "/msdk/eval/users/$USER", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalBody)},
		{"mobile get evalx", "GET", "/msdk/evalx/users/$USER", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBody)},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				for _, req := range s.requests {
					r := req
					t.Run(r.name, func(t *testing.T) {
						t.Run("success", func(t *testing.T) {
							result, body := st.DoRequest(s.request(r), p.relay)

							if assert.Equal(t, r.expectedStatus, result.StatusCode) {
								m.In(t).Assert(body, r.bodyMatcher)
								st.AssertNonStreamingHeaders(t, result.Header)
							}
						})

						t.Run("unknown mobile key", func(t *testing.T) {
							s1 := s
							s1.credential = st.UndefinedMobileKey
							result, _ := st.DoRequest(s1.request(r), p.relay)

							assert.Equal(t, http.StatusUnauthorized, result.StatusCode)
						})

						for _, user := range allBadUserTestParams {
							u := user
							t.Run(u.name, func(t *testing.T) {
								r1 := r
								r1.data = u.userJSON
								result, _ := st.DoRequest(s.request(r1), p.relay)

								assert.Equal(t, http.StatusBadRequest, result.StatusCode)
							})
						}
					})
				}
			})
		}
	})
}

func TestEndpointsEvalJSClient(t *testing.T) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
	user := lduser.NewUser("me")
	basicUserJSON, _ := json.Marshal(user)
	basicContextJSON := []byte(`{"kind": "user", "key": "me"}`)
	expectedJSEvalBody := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, false, false))
	expectedJSEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, true, false))
	expectedJSEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, true, true))

	specs := []endpointMultiTestParams{
		{"report eval", "REPORT", "/sdk/eval/$ENV/user", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalBody)},
		{"report evalx", "REPORT", "/sdk/evalx/$ENV/user", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBody)},
		{"report evalx with reasons", "REPORT", "/sdk/evalx/$ENV/user?withReasons=true", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBodyWithReasons)},
		{"get eval", "GET", "/sdk/eval/$ENV/users/$USER", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalBody)},
		{"get evalx", "GET", "/sdk/evalx/$ENV/users/$USER", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBody)},
		{"get evalx with reasons", "GET", "/sdk/evalx/$ENV/users/$USER?withReasons=true", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBodyWithReasons)},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvClientSide, st.EnvClientSideSecureMode)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				for _, req := range s.requests {
					r := req
					t.Run(r.name, func(t *testing.T) {
						t.Run("success", func(t *testing.T) {
							result, body := st.DoRequest(s.request(r), p.relay)

							if assert.Equal(t, r.expectedStatus, result.StatusCode) {
								st.AssertNonStreamingHeaders(t, result.Header)
								st.AssertExpectedCORSHeaders(t, result, s.method, "*")
								m.In(t).Assert(body, r.bodyMatcher)
							}
						})

						t.Run("secure mode - hash matches", func(t *testing.T) {
							s1 := s
							s1.credential = st.EnvClientSideSecureMode.Config.EnvID
							s1.path = st.AddQueryParam(s1.path, "h="+testclient.FakeHashForContext(user))
							result, body := st.DoRequest(s1.request(r), p.relay)

							if assert.Equal(t, r.expectedStatus, result.StatusCode) {
								st.AssertNonStreamingHeaders(t, result.Header)
								st.AssertExpectedCORSHeaders(t, result, s.method, "*")
								m.In(t).Assert(body, r.bodyMatcher)
							}
						})

						t.Run("secure mode - hash does not match", func(t *testing.T) {
							s1 := s
							s1.credential = st.EnvClientSideSecureMode.Config.EnvID
							s1.path = st.AddQueryParam(s1.path, "h=incorrect")
							result, _ := st.DoRequest(s1.request(r), p.relay)

							assert.Equal(t, http.StatusBadRequest, result.StatusCode)
						})

						t.Run("secure mode - hash not provided", func(t *testing.T) {
							s1 := s
							s1.credential = st.EnvClientSideSecureMode.Config.EnvID
							result, _ := st.DoRequest(s1.request(r), p.relay)

							assert.Equal(t, http.StatusBadRequest, result.StatusCode)
						})

						t.Run("unknown environment ID", func(t *testing.T) {
							s1 := s
							s1.credential = st.UndefinedEnvID
							result, _ := st.DoRequest(s1.request(r), p.relay)
							assert.Equal(t, http.StatusNotFound, result.StatusCode)
						})

						for _, user := range allBadUserTestParams {
							u := user
							t.Run(u.name, func(t *testing.T) {
								r1 := r
								r1.data = u.userJSON
								result, _ := st.DoRequest(s.request(r1), p.relay)

								assert.Equal(t, http.StatusBadRequest, result.StatusCode)
							})
						}

						t.Run("options", func(t *testing.T) {
							st.AssertEndpointSupportsOptionsRequest(t, p.relay, s.localURL(r), s.method)
						})
					})
				}
			})
		}
	})
}
