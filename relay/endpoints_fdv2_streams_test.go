package relay

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	"github.com/launchdarkly/ld-relay/v8/internal/credential"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/eventsource"
	ct "github.com/launchdarkly/go-configtypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fdv2StreamEndpointTestParams struct {
	endpointTestParams
	expectedEvents    []string
	expectedEventData [][]byte
}

func (s fdv2StreamEndpointTestParams) runBasicStreamTests(
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
				expectedCount := 11 // = 1 intent + 8 flags + 1 segment + 1 payload transferred
				actualCount := 0
				timer := time.NewTimer(time.Second * 3)
			L:
				for {
					select {
					case _, ok := <-eventCh:
						if !ok {
							break L
						}

						actualCount++
						if actualCount == expectedCount {
							break L
						}
					case <-timer.C:
						break L
					}
				}

				assert.Equal(t, expectedCount, actualCount, "expected to receive one event but got %d", actualCount)

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

func (s fdv2StreamEndpointTestParams) assertRequestReceivesEvent(
	t *testing.T,
	handler http.Handler,
	timeToWaitAfterEvent time.Duration,
) {
	resp := st.WithStreamRequest(t, s.request(), handler, func(eventCh <-chan eventsource.Event) {
		timer := time.NewTimer(time.Second * 3)
		defer timer.Stop()

		events := make([]eventsource.Event, 0, len(s.expectedEvents))

	L:
		for {
			select {
			case event, ok := <-eventCh:
				if !ok {
					break L
				}

				events = append(events, event)
				if len(events) == len(s.expectedEvents) {
					break L
				}
			case <-timer.C:
				break L
			}
		}

		assert.Len(t, events, len(s.expectedEvents))
		for i, expectedEvent := range s.expectedEvents {
			assert.Equal(t, expectedEvent, events[i].Event(), "expected event %d to be %s but got %s", i, expectedEvent, events[i].Event())
		}

		for i, expectedData := range s.expectedEventData {
			assert.JSONEq(t, string(expectedData), events[i].Data(), "expected event %d data to be %s but got %s", i, expectedData, events[i].Data())
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

func (s fdv2StreamEndpointTestParams) assertStreamClosesAutomatically(
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

// TODO(fdv2): To be re-enabled once mobile and client-side streaming is implemented.
// func doFDv2StreamRequestExpectingError(req *http.Request, handler http.Handler) *http.Response {
// 	w, bodyReader := st.NewStreamRecorder()
// 	handler.ServeHTTP(w, req)
// 	go func() {
// 		_, _ = io.ReadAll(bodyReader)
// 	}()
// 	return w.Result()
// }

func TestFDv2EndpointsStreamingServerSide(t *testing.T) {
	env := st.EnvMain
	sdkKey := env.Config.SDKKey

	serverIntent := subsystems.ServerIntent{
		Payload: subsystems.Payload{
			ID:     "relay-spoofed-id",
			Target: 0,
			Code:   subsystems.IntentTransferFull,
			Reason: "cant-catchup",
		},
	}
	flags := []ldmodel.FeatureFlag{
		st.Flag1ServerSide.Flag,
		st.Flag2ServerSide.Flag,
		st.Flag3ServerSideNotMobile.Flag,
		st.Flag4ClientSide.Flag,
		st.Flag5ClientSide.Flag,
		st.Flag6ClientSideNotMobile.Flag,
		st.Flag7Mobile.Flag,
		st.Flag8ContextAware.Flag,
	}
	segments := []ldmodel.Segment{
		st.Segment1,
	}

	eventData := make([][]byte, 0, len(flags)+len(segments)+2)
	j, err := serverIntent.MarshalJSON()
	assert.NoError(t, err)
	eventData = append(eventData, j)

	for _, flag := range flags {
		flagJSON, err := json.Marshal(
			ldstoretypes.ItemDescriptor{
				Version: flag.Version,
				Item:    flag,
			},
		)
		assert.NoError(t, err)

		putObject := subsystems.PutObject{
			Version: flag.Version,
			Kind:    subsystems.FlagKind,
			Key:     flag.Key,
			Object:  flagJSON,
		}
		j, err = json.Marshal(putObject)
		assert.NoError(t, err)
		eventData = append(eventData, j)
	}

	for _, segment := range segments {
		segmentJSON, err := json.Marshal(
			ldstoretypes.ItemDescriptor{
				Version: segment.Version,
				Item:    segment,
			},
		)
		assert.NoError(t, err)

		putObject := subsystems.PutObject{
			Version: segment.Version,
			Kind:    subsystems.SegmentKind,
			Key:     segment.Key,
			Object:  segmentJSON,
		}
		j, err = json.Marshal(putObject)
		assert.NoError(t, err)
		eventData = append(eventData, j)
	}

	selector := subsystems.NoSelector()
	j, err = selector.MarshalJSON()
	assert.NoError(t, err)
	eventData = append(eventData, j)

	specs := []fdv2StreamEndpointTestParams{
		// TODO(fdv2): Re-enable once flag only streaming is enabled
		// {endpointTestParams{"flags stream", "GET", "/flags", nil, sdkKey, 200, st.ExpectNoBody()}, "put", expectedFlagsData},
		{
			endpointTestParams: endpointTestParams{"all stream", "GET", "/sdk/stream", nil, sdkKey, 200, st.ExpectNoBody()}, expectedEvents: []string{
				"server-intent",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"put-object",
				"payload-transferred",
			},
			expectedEventData: eventData,
		},
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

// TODO(fdv2): Re-enable once mobile and client-side streaming is implemented.
// func TestFDv2EndpointsStreamingMobile(t *testing.T) {
// 	env := st.EnvMobile
// 	userJSON := []byte(`{"key":"me"}`)
//
// 	specs := []fdv2StreamEndpointTestParams{
// 		{
// 			endpointTestParams{"mobile ping", "GET", "/mping", nil, env.Config.MobileKey, 200, st.ExpectNoBody()},
// 			"ping", nil,
// 		},
// 		{
// 			endpointTestParams{"mobile stream GET", "GET", "/meval/$DATA", userJSON, env.Config.MobileKey, 200, st.ExpectNoBody()},
// 			"ping", nil,
// 		},
// 		{
// 			endpointTestParams{"mobile stream REPORT", "REPORT", "/meval", userJSON, env.Config.MobileKey, 200, st.ExpectNoBody()},
// 			"ping", nil,
// 		},
// 	}
//
// 	var config c.Config
// 	config.Environment = st.MakeEnvConfigs(env)
//
// 	for _, spec := range specs {
// 		s := spec
// 		s.runBasicStreamTests(t, config, st.UndefinedMobileKey, http.StatusUnauthorized)
// 	}
// }
//
// func TestFDv2EndpointsStreamingJSClient(t *testing.T) {
// 	env := st.EnvClientSide
// 	envID := env.Config.EnvID
// 	user := lduser.NewUser("me")
// 	userJSON, _ := json.Marshal(user)
//
// 	specs := []fdv2StreamEndpointTestParams{
// 		{
// 			endpointTestParams{"client-side get ping", "GET", "/ping/$ENV", nil, envID, 200, st.ExpectNoBody()},
// 			"ping", nil,
// 		},
// 		{
// 			endpointTestParams{"client-side get eval stream", "GET", "/eval/$ENV/$DATA", userJSON, envID, 200, st.ExpectNoBody()},
// 			"ping", nil,
// 		},
// 		{
// 			endpointTestParams{"client-side report eval stream", "REPORT", "/eval/$ENV", userJSON, envID, 200, st.ExpectNoBody()},
// 			"ping", nil,
// 		},
// 	}
//
// 	var config c.Config
// 	config.Environment = st.MakeEnvConfigs(st.EnvClientSide, st.EnvClientSideSecureMode)
//
// 	for _, spec := range specs {
// 		s := spec
// 		s.runBasicStreamTests(t, config, st.UndefinedEnvID, http.StatusNotFound)
// 	}
//
// 	withStartedRelay(t, config, func(p relayTestParams) {
// 		for _, spec := range specs {
// 			s := spec
// 			t.Run(s.name, func(t *testing.T) {
// 				if s.data != nil {
// 					t.Run("secure mode - hash matches", func(t *testing.T) {
// 						s1 := s
// 						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
// 						s1.path = st.AddQueryParam(s1.path, "h="+testclient.FakeHashForContext(user))
// 						s1.assertRequestReceivesEvent(t, p.relay, 0)
// 					})
//
// 					t.Run("secure mode - hash does not match", func(t *testing.T) {
// 						s1 := s
// 						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
// 						s1.path = st.AddQueryParam(s1.path, "h=incorrect")
// 						result := doFDv2StreamRequestExpectingError(s1.request(), p.relay)
//
// 						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
// 					})
//
// 					t.Run("secure mode - hash not provided", func(t *testing.T) {
// 						s1 := s
// 						s1.credential = st.EnvClientSideSecureMode.Config.EnvID
// 						result := doFDv2StreamRequestExpectingError(s1.request(), p.relay)
//
// 						assert.Equal(t, http.StatusBadRequest, result.StatusCode)
// 					})
// 				}
//
// 				t.Run("options", func(t *testing.T) {
// 					st.AssertEndpointSupportsOptionsRequest(t, p.relay, s.localURL(), s.method)
// 				})
// 			})
// 		}
// 	})
// }
