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
	"github.com/launchdarkly/ld-relay/v6/internal/streams"
)

type streamEndpointTestParams struct {
	endpointTestParams
	expectedEvent string
	expectedData  []byte
}

func (s streamEndpointTestParams) runBasicStreamTests(
	t *testing.T,
	baseConfig c.Config,
	invalidCredential c.SDKCredential,
	invalidCredentialExpectedStatus int,
) {
	configWithoutTimeLimit := baseConfig
	configWithoutTimeLimit.Main.MaxClientConnectionTime = ct.OptDuration{}

	relayTest(configWithoutTimeLimit, func(p relayTestParams) {
		t.Run("success", func(t *testing.T) {
			s.assertRequestReceivesEvent(t, p.relay, 200*time.Millisecond)
		})

		t.Run("invalid credential", func(t *testing.T) {
			s1 := s
			s1.credential = invalidCredential
			result, _ := doRequest(s1.request(), p.relay)

			assert.Equal(t, invalidCredentialExpectedStatus, result.StatusCode)
		})
	})

	relayTest(configWithoutTimeLimit, func(p relayTestParams) {
		t.Run("stream is closed if environment is removed", func(t *testing.T) {
			env := p.relay.core.GetEnvironment(s.credential)
			require.NotNil(t, env)

			withStreamRequest(t, s.request(), p.relay, func(eventCh <-chan eventsource.Event) {
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

				p.relay.core.RemoveEnvironment(c.SDKKey(env.GetCredentials().SDKKey))

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

	relayTest(configWithTimeLimit, func(p relayTestParams) {
		t.Run("connection time limit", func(t *testing.T) {
			s.assertStreamClosesAutomatically(t, p.relay, maxConnTime)
		})
	})
}

func (s streamEndpointTestParams) assertRequestReceivesEvent(
	t *testing.T,
	relay *Relay,
	timeToWaitAfterEvent time.Duration,
) {
	resp := withStreamRequest(t, s.request(), relay, func(eventCh <-chan eventsource.Event) {
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
	relay *Relay,
	shouldCloseAfter time.Duration,
) {
	maxWait := time.NewTimer(shouldCloseAfter + time.Second)
	defer maxWait.Stop()
	startTime := time.Now()
	_ = withStreamRequest(t, s.request(), relay, func(eventCh <-chan eventsource.Event) {
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

func TestRelayServerSideStreams(t *testing.T) {
	env := testEnvMain
	sdkKey := env.config.SDKKey
	expectedAllData := []byte(streams.MakeServerSidePutEvent(allData).Data())
	expectedFlagsData := []byte(streams.MakeServerSideFlagsOnlyPutEvent(allData).Data())

	specs := []streamEndpointTestParams{
		{endpointTestParams{"flags stream", "GET", "/flags", nil, sdkKey, 200, nil}, "put", expectedFlagsData},
		{endpointTestParams{"all stream", "GET", "/all", nil, sdkKey, 200, nil}, "put", expectedAllData},
	}

	var config c.Config
	config.Environment = makeEnvConfigs(env)

	for _, s := range specs {
		t.Run(s.name, func(t *testing.T) {
			s.runBasicStreamTests(t, config, undefinedSDKKey, http.StatusUnauthorized)
		})
	}
}

func TestRelayMobileStreams(t *testing.T) {
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
		s.runBasicStreamTests(t, config, undefinedMobileKey, http.StatusUnauthorized)
	}
}

func TestRelayJSClientStreams(t *testing.T) {
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
		s.runBasicStreamTests(t, config, undefinedEnvID, http.StatusNotFound)
	}

	relayTest(config, func(p relayTestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				if s.data != nil {
					t.Run("secure mode - hash matches", func(t *testing.T) {
						s1 := s
						s1.credential = testEnvClientSideSecureMode.config.EnvID
						s1.path = addQueryParam(s1.path, "h="+fakeHashForUser(user))
						s1.assertRequestReceivesEvent(t, p.relay, 0)
					})

					t.Run("secure mode - hash does not match", func(t *testing.T) {
						s1 := s
						s1.credential = testEnvClientSideSecureMode.config.EnvID
						s1.path = addQueryParam(s1.path, "h=incorrect")
						result := doStreamRequestExpectingError(s1.request(), p.relay)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})

					t.Run("secure mode - hash not provided", func(t *testing.T) {
						s1 := s
						s1.credential = testEnvClientSideSecureMode.config.EnvID
						result := doStreamRequestExpectingError(s1.request(), p.relay)

						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
					})
				}

				t.Run("options", func(t *testing.T) {
					assertEndpointSupportsOptionsRequest(t, p.relay, s.localURL(), s.method)
				})
			})
		}
	})
}
