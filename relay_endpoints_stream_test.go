package relay

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"

	"github.com/launchdarkly/eventsource"
	ct "github.com/launchdarkly/go-configtypes"
	c "github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/core/streams"
)

func DoStreamEndpointsTests(t *testing.T, constructor TestConstructor) {
	constructor.RunTest(t, "server-side", DoServerSideStreamsTest)
	constructor.RunTest(t, "mobile", DoMobileStreamsTest)
	constructor.RunTest(t, "JS client", DoJSClientStreamsTest)
}

type streamEndpointTestParams struct {
	endpointTestParams
	expectedEvent string
	expectedData  []byte
}

func (s streamEndpointTestParams) runBasicStreamTests(
	t *testing.T,
	baseConfig c.Config,
	constructor TestConstructor,
	invalidCredential c.SDKCredential,
	invalidCredentialExpectedStatus int,
) {
	configWithoutTimeLimit := baseConfig
	configWithoutTimeLimit.Main.MaxClientConnectionTime = ct.OptDuration{}

	DoTest(configWithoutTimeLimit, constructor, func(p TestParams) {
		t.Run("success", func(t *testing.T) {
			s.assertRequestReceivesEvent(t, p.Handler, 200*time.Millisecond)
		})

		t.Run("invalid credential", func(t *testing.T) {
			s1 := s
			s1.credential = invalidCredential
			result, _ := st.DoRequest(s1.request(), p.Handler)

			assert.Equal(t, invalidCredentialExpectedStatus, result.StatusCode)
		})
	})

	DoTest(configWithoutTimeLimit, constructor, func(p TestParams) {
		t.Run("stream is closed if environment is removed", func(t *testing.T) {
			env := p.Core.GetEnvironment(s.credential)
			require.NotNil(t, env)

			withStreamRequest(t, s.request(), p.Handler, func(eventCh <-chan eventsource.Event) {
				select {
				case event := <-eventCh:
					if event == nil {
						assert.Fail(t, "stream closed unexpectedly")
						return
					}
				case <-time.After(time.Second * 3):
					assert.Fail(t, "timed out waiting for initial event")
					return
				}

				p.Core.RemoveEnvironment(c.SDKKey(env.GetCredentials().SDKKey))

				select {
				case event := <-eventCh:
					if event != nil {
						assert.Fail(t, "expected end of stream, got another event")
					}
				case <-time.After(time.Second):
					assert.Fail(t, "timed out waiting for stream to be closed")
				}
			})
		})
	})

	maxConnTime := 100 * time.Millisecond
	configWithTimeLimit := baseConfig
	configWithTimeLimit.Main.MaxClientConnectionTime = ct.NewOptDuration(maxConnTime)

	DoTest(configWithTimeLimit, constructor, func(p TestParams) {
		t.Run("connection time limit", func(t *testing.T) {
			s.assertStreamClosesAutomatically(t, p.Handler, maxConnTime)
		})
	})
}

func (s streamEndpointTestParams) assertRequestReceivesEvent(
	t *testing.T,
	handler http.Handler,
	timeToWaitAfterEvent time.Duration,
) {
	resp := withStreamRequest(t, s.request(), handler, func(eventCh <-chan eventsource.Event) {
		eventTimeout := time.NewTimer(time.Second * 3)
		defer eventTimeout.Stop()
		select {
		case event := <-eventCh:
			if event == nil {
				assert.Fail(t, "stream closed unexpectedly")
				return
			}
			assert.Equal(t, s.expectedEvent, event.Event())
			if s.expectedData != nil {
				assert.JSONEq(t, string(s.expectedData), event.Data())
			}
			// Now wait a little longer to make sure the stream doesn't close unexpectedly, to verify that
			// we did not mistakenly enable the max connection time feature.
			if timeToWaitAfterEvent > 0 {
				select {
				case event := <-eventCh:
					if event == nil {
						assert.Fail(t, "stream closed unexpectedly")
					} else {
						assert.Fail(t, "received unexpected second event")
					}
				case <-time.After(timeToWaitAfterEvent):
				}
			}
		case <-eventTimeout.C:
			assert.Fail(t, "timed out waiting for event")
		}
	})
	if _, ok := s.credential.(c.EnvironmentID); ok {
		assertExpectedCORSHeaders(t, resp, s.method, "*")
	}
}

func (s streamEndpointTestParams) assertStreamClosesAutomatically(
	t *testing.T,
	handler http.Handler,
	shouldCloseAfter time.Duration,
) {
	maxWait := time.NewTimer(shouldCloseAfter + time.Second)
	defer maxWait.Stop()
	startTime := time.Now()
	_ = withStreamRequest(t, s.request(), handler, func(eventCh <-chan eventsource.Event) {
		for {
			select {
			case event := <-eventCh:
				if event == nil { // stream was closed
					timeUntilClosed := time.Now().Sub(startTime)
					if timeUntilClosed < shouldCloseAfter {
						assert.Fail(t, "stream closed too soon", "expected %s but closed after %s",
							shouldCloseAfter, timeUntilClosed)
					}
					return
				}
			case <-maxWait.C:
				assert.Fail(t, "timed out waiting for stream to close")
				return
			}
		}
	})
}

func DoServerSideStreamsTest(t *testing.T, constructor TestConstructor) {
	env := testEnvMain
	sdkKey := env.config.SDKKey
	expectedAllData := []byte(streams.MakeServerSidePutEvent(st.AllData).Data())
	expectedFlagsData := []byte(streams.MakeServerSideFlagsOnlyPutEvent(st.AllData).Data())

	specs := []streamEndpointTestParams{
		{endpointTestParams{"flags stream", "GET", "/flags", nil, sdkKey, 200, nil}, "put", expectedFlagsData},
		{endpointTestParams{"all stream", "GET", "/all", nil, sdkKey, 200, nil}, "put", expectedAllData},
	}

	var config c.Config
	config.Environment = makeEnvConfigs(env)

	for _, s := range specs {
		t.Run(s.name, func(t *testing.T) {
			s.runBasicStreamTests(t, config, constructor, undefinedSDKKey, http.StatusUnauthorized)
		})
	}
}

func DoMobileStreamsTest(t *testing.T, constructor TestConstructor) {
	env := testEnvMobile
	userJSON := []byte(`{"key":"me"}`)

	specs := []streamEndpointTestParams{
		{endpointTestParams{"mobile ping", "GET", "/mping", nil, env.config.MobileKey, 200, nil},
			"ping", nil},
		{endpointTestParams{"mobile stream GET", "GET", "/meval/$DATA", userJSON, env.config.MobileKey, 200, nil},
			"ping", nil},
		{endpointTestParams{"mobile stream REPORT", "REPORT", "/meval", userJSON, env.config.MobileKey, 200, nil},
			"ping", nil},
	}

	var config c.Config
	config.Environment = makeEnvConfigs(env)

	for _, s := range specs {
		s.runBasicStreamTests(t, config, constructor, undefinedMobileKey, http.StatusUnauthorized)
	}
}

func DoJSClientStreamsTest(t *testing.T, constructor TestConstructor) {
	env := testEnvClientSide
	envID := env.config.EnvID
	user := lduser.NewUser("me")
	userJSON, _ := json.Marshal(user)

	specs := []streamEndpointTestParams{
		{endpointTestParams{"client-side get ping", "GET", "/ping/$ENV", nil, envID, 200, nil},
			"ping", nil},
		{endpointTestParams{"client-side get eval stream", "GET", "/eval/$ENV/$DATA", userJSON, envID, 200, nil},
			"ping", nil},
		{endpointTestParams{"client-side report eval stream", "REPORT", "/eval/$ENV", userJSON, envID, 200, nil},
			"ping", nil},
	}

	var config c.Config
	config.Environment = makeEnvConfigs(testEnvClientSide, testEnvClientSideSecureMode)

	for _, s := range specs {
		s.runBasicStreamTests(t, config, constructor, undefinedEnvID, http.StatusNotFound)
	}

	DoTest(config, constructor, func(p TestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				if s.data != nil {
					t.Run("secure mode - hash matches", func(t *testing.T) {
						s1 := s
						s1.credential = testEnvClientSideSecureMode.config.EnvID
						s1.path = st.AddQueryParam(s1.path, "h="+testclient.FakeHashForUser(user))
						s1.assertRequestReceivesEvent(t, p.Handler, 0)
					})

					t.Run("secure mode - hash does not match", func(t *testing.T) {
						s1 := s
						s1.credential = testEnvClientSideSecureMode.config.EnvID
						s1.path = st.AddQueryParam(s1.path, "h=incorrect")
						result := doStreamRequestExpectingError(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})

					t.Run("secure mode - hash not provided", func(t *testing.T) {
						s1 := s
						s1.credential = testEnvClientSideSecureMode.config.EnvID
						result := doStreamRequestExpectingError(s1.request(), p.Handler)

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
