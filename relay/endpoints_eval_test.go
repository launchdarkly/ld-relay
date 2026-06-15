package relay

import (
	"encoding/json"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	"github.com/launchdarkly/go-sdk-common/v3/lduser"
	"github.com/launchdarkly/go-test-helpers/v3/jsonhelpers"
	m "github.com/launchdarkly/go-test-helpers/v3/matchers"

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
	expectedServerEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, false))
	expectedServerEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.AllFlags, true))

	specs := []endpointMultiTestParams{
		{"server-side context report evalx", "REPORT", "/sdk/evalx/context", sdkKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedServerEvalxBody)},
		{"server-side context report evalx with reasons", "REPORT", "/sdk/evalx/context?withReasons=true", sdkKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedServerEvalxBodyWithReasons)},
		{"server-side user report evalx", "REPORT", "/sdk/evalx/user", sdkKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedServerEvalxBody)},
		{"server-side user report evalx with reasons", "REPORT", "/sdk/evalx/user?withReasons=true", sdkKey,
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
	expectedMobileEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, false))
	expectedMobileEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.MobileFlags, true))

	specs := []endpointMultiTestParams{
		{"mobile context report evalx", "REPORT", "/msdk/evalx/context", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBody)},
		{"mobile context report evalx with reasons", "REPORT", "/msdk/evalx/context?withReasons=true", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBodyWithReasons)},
		{"mobile user report evalx", "REPORT", "/msdk/evalx/user", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBody)},
		{"mobile user report evalx with reasons", "REPORT", "/msdk/evalx/user?withReasons=true", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBodyWithReasons)},
		{"mobile context get evalx", "GET", "/msdk/evalx/contexts/$USER", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBody)},
		{"mobile context get evalx with reasons", "GET", "/msdk/evalx/contexts/$USER?withReasons=true", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBodyWithReasons)},
		{"mobile user get evalx", "GET", "/msdk/evalx/users/$USER", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBody)},
		{"mobile user get evalx with reasons", "GET", "/msdk/evalx/users/$USER?withReasons=true", mobileKey,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedMobileEvalxBodyWithReasons)},
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
	expectedJSEvalxBody := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, false))
	expectedJSEvalxBodyWithReasons := st.ExpectJSONBody(st.MakeEvalBody(st.ClientSideFlags, true))

	specs := []endpointMultiTestParams{
		{"report context evalx", "REPORT", "/sdk/evalx/$ENV/context", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBody)},
		{"report context evalx with reasons", "REPORT", "/sdk/evalx/$ENV/context?withReasons=true", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBodyWithReasons)},
		{"report user evalx", "REPORT", "/sdk/evalx/$ENV/user", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBody)},
		{"report user evalx with reasons", "REPORT", "/sdk/evalx/$ENV/user?withReasons=true", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBodyWithReasons)},
		{"get context evalx", "GET", "/sdk/evalx/$ENV/contexts/$USER", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBody)},
		{"get context evalx with reasons", "GET", "/sdk/evalx/$ENV/contexts/$USER?withReasons=true", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBodyWithReasons)},
		{"get user evalx", "GET", "/sdk/evalx/$ENV/users/$USER", envID,
			makeEndpointTestPerRequestParams(basicUserJSON, basicContextJSON, expectedJSEvalxBody)},
		{"get user evalx with reasons", "GET", "/sdk/evalx/$ENV/users/$USER?withReasons=true", envID,
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
