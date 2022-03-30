package testsuites

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/internal/core/streams"

	"github.com/launchdarkly/eventsource"
	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/lduser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	DoTest(t, configWithoutTimeLimit, constructor, func(p TestParams) {
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

	DoTest(t, configWithoutTimeLimit, constructor, func(p TestParams) {
		t.Run("stream is closed if environment is removed", func(t *testing.T) {
			env, inited := p.Core.GetEnvironment(s.credential)
			require.NotNil(t, env)
			require.True(t, inited)

			st.WithStreamRequest(t, s.request(), p.Handler, func(eventCh <-chan eventsource.Event) {
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

				p.Core.RemoveEnvironment(env)

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

	DoTest(t, configWithTimeLimit, constructor, func(p TestParams) {
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
	resp := st.WithStreamRequest(t, s.request(), handler, func(eventCh <-chan eventsource.Event) {
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
		st.AssertExpectedCORSHeaders(t, resp, s.method, "*")
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
	_ = st.WithStreamRequest(t, s.request(), handler, func(eventCh <-chan eventsource.Event) {
		for {
			select {
			case event := <-eventCh:
				if event == nil { // stream was closed
					timeUntilClosed := time.Since(startTime)
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

func doStreamRequestExpectingError(req *http.Request, handler http.Handler) *http.Response {
	w, bodyReader := st.NewStreamRecorder()
	handler.ServeHTTP(w, req)
	go func() {
		_, _ = ioutil.ReadAll(bodyReader)
	}()
	return w.Result()
}

func DoServerSideStreamsTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvMain
	sdkKey := env.Config.SDKKey
	expectedAllData := []byte(streams.MakeServerSidePutEvent(st.AllData).Data())
	expectedFlagsData := []byte(streams.MakeServerSideFlagsOnlyPutEvent(st.AllData).Data())

	specs := []streamEndpointTestParams{
		{endpointTestParams{"flags stream", "GET", "/flags", nil, sdkKey, 200, st.ExpectNoBody()}, "put", expectedFlagsData},
		{endpointTestParams{"all stream", "GET", "/all", nil, sdkKey, 200, st.ExpectNoBody()}, "put", expectedAllData},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	for _, spec := range specs {
		s := spec
		t.Run(s.name, func(t *testing.T) {
			s.runBasicStreamTests(t, config, constructor, st.UndefinedSDKKey, http.StatusUnauthorized)
		})
	}
}

func DoMobileStreamsTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvMobile
	userJSON := []byte(`{"key":"me"}`)

	specs := []streamEndpointTestParams{
		{endpointTestParams{"mobile ping", "GET", "/mping", nil, env.Config.MobileKey, 200, st.ExpectNoBody()},
			"ping", nil},
		{endpointTestParams{"mobile stream GET", "GET", "/meval/$DATA", userJSON, env.Config.MobileKey, 200, st.ExpectNoBody()},
			"ping", nil},
		{endpointTestParams{"mobile stream REPORT", "REPORT", "/meval", userJSON, env.Config.MobileKey, 200, st.ExpectNoBody()},
			"ping", nil},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	for _, spec := range specs {
		s := spec
		s.runBasicStreamTests(t, config, constructor, st.UndefinedMobileKey, http.StatusUnauthorized)
	}
}

func DoJSClientStreamsTest(t *testing.T, constructor TestConstructor) {
	env := st.EnvClientSide
	envID := env.Config.EnvID
	user := lduser.NewUser("me")
	userJSON, _ := json.Marshal(user)

	specs := []streamEndpointTestParams{
		{endpointTestParams{"client-side get ping", "GET", "/ping/$ENV", nil, envID, 200, st.ExpectNoBody()},
			"ping", nil},
		{endpointTestParams{"client-side get eval stream", "GET", "/eval/$ENV/$DATA", userJSON, envID, 200, st.ExpectNoBody()},
			"ping", nil},
		{endpointTestParams{"client-side report eval stream", "REPORT", "/eval/$ENV", userJSON, envID, 200, st.ExpectNoBody()},
			"ping", nil},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvClientSide, st.EnvClientSideSecureMode)

	for _, spec := range specs {
		s := spec
		s.runBasicStreamTests(t, config, constructor, st.UndefinedEnvID, http.StatusNotFound)
	}

	DoTest(t, config, constructor, func(p TestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				if s.data != nil {
					t.Run("secure mode - hash matches", func(t *testing.T) {
						s1 := s
						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
						s1.path = st.AddQueryParam(s1.path, "h="+testclient.FakeHashForContext(user))
						s1.assertRequestReceivesEvent(t, p.Handler, 0)
					})

					t.Run("secure mode - hash does not match", func(t *testing.T) {
						s1 := s
						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
						s1.path = st.AddQueryParam(s1.path, "h=incorrect")
						result := doStreamRequestExpectingError(s1.request(), p.Handler)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})

					t.Run("secure mode - hash not provided", func(t *testing.T) {
						s1 := s
						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
						result := doStreamRequestExpectingError(s1.request(), p.Handler)

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
