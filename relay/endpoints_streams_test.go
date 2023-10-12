package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/eventsource"
	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/lduser"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type streamEndpointTestParams struct {
	endpointTestParams
	expectedEvent string
	expectedData  []byte
}

func (s streamEndpointTestParams) runBasicStreamTests(
	t *testing.T,
	baseConfig c.Config,
	invalidCredential credential.SDKCredential,
	invalidCredentialExpectedStatus int,
) {
	configWithoutTimeLimit := baseConfig
	configWithoutTimeLimit.Main.MaxClientConnectionTime = ct.OptDuration{}

	withStartedRelay(t, configWithoutTimeLimit, func(p relayTestParams) {
		t.Run("success", func(t *testing.T) {
			s.assertRequestReceivesEvent(t, p.relay, 200*time.Millisecond)
		})

		t.Run("invalid credential", func(t *testing.T) {
			s1 := s
			s1.credential = invalidCredential
			result, _ := st.DoRequest(s1.request(), p.relay)

			assert.Equal(t, invalidCredentialExpectedStatus, result.StatusCode)
		})
	})

	withStartedRelay(t, configWithoutTimeLimit, func(p relayTestParams) {
		t.Run("stream is closed if environment is removed", func(t *testing.T) {
			env, err := p.relay.getEnvironment(sdkauth.New(s.credential))
			require.NotNil(t, env)
			require.Nil(t, err)

			st.WithStreamRequest(t, s.request(), p.relay, func(eventCh <-chan eventsource.Event) {
				_ = helpers.RequireValue(t, eventCh, time.Second*3, "timed out waiting for initial event")

				p.relay.removeEnvironment(sdkauth.New(s.credential))

				// The WithStreamRequest helper adds a nil value at the end of the stream
				endOfStreamMarker := helpers.RequireValue(t, eventCh, time.Second, "timed out waiting for stream to be closed")
				require.Nil(t, endOfStreamMarker)
			})
		})
	})

	maxConnTime := 100 * time.Millisecond
	configWithTimeLimit := baseConfig
	configWithTimeLimit.Main.MaxClientConnectionTime = ct.NewOptDuration(maxConnTime)

	withStartedRelay(t, configWithTimeLimit, func(p relayTestParams) {
		t.Run("connection time limit", func(t *testing.T) {
			s.assertStreamClosesAutomatically(t, p.relay, maxConnTime)
		})
	})
}

func (s streamEndpointTestParams) assertRequestReceivesEvent(
	t *testing.T,
	handler http.Handler,
	timeToWaitAfterEvent time.Duration,
) {
	resp := st.WithStreamRequest(t, s.request(), handler, func(eventCh <-chan eventsource.Event) {
		event := helpers.RequireValue(t, eventCh, time.Second*3, "timed out waiting for event")
		assert.Equal(t, s.expectedEvent, event.Event())
		if s.expectedData != nil {
			assert.JSONEq(t, string(s.expectedData), event.Data())
		}
		// Now wait a little longer to make sure the stream doesn't close unexpectedly, to verify that
		// we did not mistakenly enable the max connection time feature.
		if timeToWaitAfterEvent > 0 {
			event, _, closed := helpers.TryReceive(eventCh, timeToWaitAfterEvent)
			if closed {
				assert.Fail(t, "stream closed unexpectedly")
			} else if event != nil {
				assert.Fail(t, "received unexpected second event")
			}
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
		_, _ = io.ReadAll(bodyReader)
	}()
	return w.Result()
}

func TestEndpointsStreamingServerSide(t *testing.T) {
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
			s.runBasicStreamTests(t, config, st.UndefinedSDKKey, http.StatusUnauthorized)
		})
	}
}

func TestEndpointsStreamingMobile(t *testing.T) {
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
		s.runBasicStreamTests(t, config, st.UndefinedMobileKey, http.StatusUnauthorized)
	}
}

func TestEndpointsStreamingJSClient(t *testing.T) {
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
		s.runBasicStreamTests(t, config, st.UndefinedEnvID, http.StatusNotFound)
	}

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				if s.data != nil {
					t.Run("secure mode - hash matches", func(t *testing.T) {
						s1 := s
						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
						s1.path = st.AddQueryParam(s1.path, "h="+testclient.FakeHashForContext(user))
						s1.assertRequestReceivesEvent(t, p.relay, 0)
					})

					t.Run("secure mode - hash does not match", func(t *testing.T) {
						s1 := s
						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
						s1.path = st.AddQueryParam(s1.path, "h=incorrect")
						result := doStreamRequestExpectingError(s1.request(), p.relay)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})

					t.Run("secure mode - hash not provided", func(t *testing.T) {
						s1 := s
						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
						result := doStreamRequestExpectingError(s1.request(), p.relay)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}

				t.Run("options", func(t *testing.T) {
					st.AssertEndpointSupportsOptionsRequest(t, p.relay, s.localURL(), s.method)
				})
			})
		}
	})
}
