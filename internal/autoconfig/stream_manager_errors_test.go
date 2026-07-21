package autoconfig

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
)

func eventShouldCauseStreamRestart(t *testing.T, event httphelpers.SSEEvent) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh
		p.stream.Enqueue(event)
		select {
		case <-p.messageHandler.received:
			require.Fail(t, "received unexpected message")
		case <-p.requestsCh: // got expected stream restart
			p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "malformed JSON")
		case <-time.After(time.Second):
			require.Fail(t, "timed out waiting for stream restart")
		}
	})
}

// A credential payload that is valid JSON and a structurally valid event, but whose credential set
// cannot be built (e.g. an undefined anchor SDK key, or mobile keys with no designated primary), must
// be caught at the parse boundary: the previous state is preserved (no AddEnvironment/UpdateEnvironment
// dispatched) and the stream is restarted so the backend resends a fresh put (design §9). This is
// verified for both patch and put, since both paths run the validation before the version is recorded.
func TestMalformedCredentialPayloadCausesStreamRestart(t *testing.T) {
	// Each shape is a distinct way BuildAcceptedSet rejects a structurally malformed payload; all ride
	// the same malformed-payload machinery.
	malformedShapes := []struct {
		name string
		make func(envfactory.EnvironmentRep) envfactory.EnvironmentRep
	}{
		{
			name: "undefined anchor SDK key",
			make: func(env envfactory.EnvironmentRep) envfactory.EnvironmentRep {
				env.SDKKey = envfactory.SDKKeyRep{Value: config.SDKKey("")}
				return env
			},
		},
		{
			name: "mobile keys without a designated primary",
			make: func(env envfactory.EnvironmentRep) envfactory.EnvironmentRep {
				env.MobKey = config.MobileKey("")                                                // no primary designated...
				env.MobileKeys = []envfactory.ConcurrentKeyRep{{Key: "mob-1", Value: "mobkey1"}} // ...but non-empty
				return env
			},
		},
	}

	for _, shape := range malformedShapes {
		t.Run(shape.name, func(t *testing.T) {
			malformedEnv := shape.make(testEnv1)

			t.Run("patch", func(t *testing.T) {
				streamManagerTest(t, nil, func(p streamManagerTestParams) {
					p.startStream()
					<-p.requestsCh
					p.stream.Enqueue(makePatchEnvEvent(malformedEnv))
					select {
					case m := <-p.messageHandler.received:
						require.Failf(t, "unexpected message",
							"must not dispatch for a malformed payload, got %s", m)
					case <-p.requestsCh: // reconnect request == stream restart
						p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "malformed credential payload")
					case <-time.After(time.Second):
						require.Fail(t, "timed out waiting for stream restart")
					}
				})
			})

			t.Run("put", func(t *testing.T) {
				streamManagerTest(t, nil, func(p streamManagerTestParams) {
					p.startStream()
					<-p.requestsCh
					p.stream.Enqueue(makeEnvPutEvent(malformedEnv))
					// The malformed env is skipped (no add/update); a put still reports ReceivedAllEnvironments,
					// which we tolerate. We require that the stream restarts and that no add/update is dispatched.
					deadline := time.After(2 * time.Second)
					for {
						select {
						case m := <-p.messageHandler.received:
							if m.add != nil || m.update != nil {
								require.Failf(t, "unexpected message",
									"must not dispatch add/update for a malformed payload, got %s", m)
							}
						case <-p.requestsCh: // reconnect request == stream restart
							p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "malformed credential payload")
							return
						case <-deadline:
							require.Fail(t, "timed out waiting for stream restart")
						}
					}
				})
			})
		})
	}
}

// A put carrying malformed credential payloads must not corrupt the persistent cache: valid envs are
// updated, malformed envs keep their previously-cached entry, and an all-malformed put must not wipe
// the cache (the regression — SetAll is a full replace, so writing a filtered/empty set erased it).
func TestMalformedCredentialPayloadPreservesEnvironmentCache(t *testing.T) {
	malformed := func(env envfactory.EnvironmentRep) envfactory.EnvironmentRep {
		env.SDKKey = envfactory.SDKKeyRep{Value: config.SDKKey("")} // undefined anchor
		return env
	}
	// updated gives env a distinct SDK key value and a bumped version, so the test can tell a freshly
	// persisted update apart from the seeded value.
	updated := func(env envfactory.EnvironmentRep, newKey config.SDKKey) envfactory.EnvironmentRep {
		env.SDKKey = envfactory.SDKKeyRep{Value: newKey}
		env.Version++
		return env
	}
	seedBothEnvs := func(t *testing.T, p streamManagerTestParams, cache *recordingCache) {
		p.stream.Enqueue(makeEnvPutEvent(testEnv1, testEnv2))
		require.Eventually(t, func() bool {
			ids := cache.cachedEnvIDs()
			return ids[testEnv1.EnvID] && ids[testEnv2.EnvID]
		}, time.Second, 10*time.Millisecond, "both envs should be cached after the clean put")
	}

	t.Run("valid env is updated, malformed env keeps its previous value", func(t *testing.T) {
		cache := &recordingCache{}
		streamManagerTestWithCache(t, nil, cache, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			seedBothEnvs(t, p, cache)

			// env1 carries a valid update to a new SDK key; env2 is malformed in the same put.
			p.stream.Enqueue(makeEnvPutEvent(updated(testEnv1, "sdkkey1-rotated"), malformed(testEnv2)))
			_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")

			env1, ok1 := cache.cachedEnv(testEnv1.EnvID)
			require.True(t, ok1, "the valid env must remain cached")
			assert.Equal(t, config.SDKKey("sdkkey1-rotated"), env1.SDKKey.Value,
				"the valid env's update must be persisted")

			env2, ok2 := cache.cachedEnv(testEnv2.EnvID)
			require.True(t, ok2, "the malformed env must keep its previously-cached entry, not be dropped")
			assert.Equal(t, testEnv2.SDKKey.Value, env2.SDKKey.Value,
				"the malformed env must retain its previous (valid) cached value, not the malformed one")
		})
	})

	t.Run("all-malformed put does not wipe the cache", func(t *testing.T) {
		cache := &recordingCache{}
		streamManagerTestWithCache(t, nil, cache, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			seedBothEnvs(t, p, cache)

			p.stream.Enqueue(makeEnvPutEvent(malformed(testEnv1), malformed(testEnv2)))
			_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")

			env1, ok1 := cache.cachedEnv(testEnv1.EnvID)
			env2, ok2 := cache.cachedEnv(testEnv2.EnvID)
			require.True(t, ok1 && ok2, "an all-malformed put must not wipe the cache")
			assert.Equal(t, testEnv1.SDKKey.Value, env1.SDKKey.Value, "env1 keeps its previous value")
			assert.Equal(t, testEnv2.SDKKey.Value, env2.SDKKey.Value, "env2 keeps its previous value")
		})
	})

	t.Run("mixed put on an empty cache still persists the valid env", func(t *testing.T) {
		cache := &recordingCache{}
		streamManagerTestWithCache(t, nil, cache, func(p streamManagerTestParams) {
			p.startStream()
			<-p.requestsCh
			// No seed: the cache is empty (GetAll returns nil, not an error). A first put mixing a valid
			// and a malformed env must still persist the valid one.
			p.stream.Enqueue(makeEnvPutEvent(testEnv1, malformed(testEnv2)))
			_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")

			env1, ok1 := cache.cachedEnv(testEnv1.EnvID)
			require.True(t, ok1, "the valid env must be persisted even on an empty cache")
			assert.Equal(t, testEnv1.SDKKey.Value, env1.SDKKey.Value)
			_, ok2 := cache.cachedEnv(testEnv2.EnvID)
			assert.False(t, ok2, "the malformed env has no prior cached value, so it is omitted")
		})
	})
}

func TestMalformedJSONInEventCausesStreamRestart(t *testing.T) {
	t.Run("put", func(t *testing.T) {
		event := httphelpers.SSEEvent{Event: PutEvent, Data: malformedJSON}
		eventShouldCauseStreamRestart(t, event)
	})

	t.Run("patch", func(t *testing.T) {
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: malformedJSON}
		eventShouldCauseStreamRestart(t, event)
	})

	t.Run("delete", func(t *testing.T) {
		event := httphelpers.SSEEvent{Event: DeleteEvent, Data: malformedJSON}
		eventShouldCauseStreamRestart(t, event)
	})
}

func TestWellFormedJSONThatIsNotWellFormedEventDataCausesStreamRestart(t *testing.T) {
	t.Run("put", func(t *testing.T) {
		t.Run("without filters", func(t *testing.T) {
			json := `{"path": "/", "data": {"environments": {"envid1": 999}}}`
			event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
		t.Run("with filters", func(t *testing.T) {
			json := `{"path": "/", "data": {"environments": {"envid1": 999}, "filters": {"filter1":999}}}`
			event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
	})

	t.Run("patch", func(t *testing.T) {
		t.Run("environments", func(t *testing.T) {
			json := `{"path": "/environments/envid1","data": 999}`
			event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
		t.Run("filters", func(t *testing.T) {
			json := `{"path": "/filters/filterid1","data": 999}`
			event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
			eventShouldCauseStreamRestart(t, event)
		})
	})

	t.Run("delete", func(t *testing.T) {
		json := `{"path": 999}`
		event := httphelpers.SSEEvent{Event: DeleteEvent, Data: json}
		eventShouldCauseStreamRestart(t, event)
	})
}

func errorShouldCauseReconnect(t *testing.T, errorProducingHandler http.Handler, expectedWarning string) {
	initialEvent := makeEnvPutEvent(testEnv1)
	streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
	defer stream.Close()
	handler := httphelpers.SequentialHandler(
		errorProducingHandler, // first request will get this
		streamHandler,         // request after reconnect will get this
	)
	streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh // first request
		_ = helpers.RequireValue(t, p.requestsCh, time.Second, "timed out waiting for stream restart")
		p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, expectedWarning)
		msg := p.requireMessage()
		assert.NotNil(t, msg.add)
		p.requireReceivedAllMessage()
	})
}

func TestReconnectAfterRecoverableHTTPError(t *testing.T) {
	for _, status := range []int{400, 500, 503} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			errorShouldCauseReconnect(t, httphelpers.HandlerWithStatus(status), fmt.Sprintf("HTTP error %d", status))
		})
	}
}

func TestReconnectAfterNetworkError(t *testing.T) {
	errorShouldCauseReconnect(t, httphelpers.BrokenConnectionHandler(), "Unexpected error")
}

func TestNoReconnectAfterUnrecoverableHTTPError(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			initialEvent := makeEnvPutEvent(testEnv1)
			streamHandler, stream := httphelpers.SSEHandler(&initialEvent)
			defer stream.Close()
			errorProducingHandler := httphelpers.HandlerWithStatus(status)
			handler := httphelpers.SequentialHandler(
				errorProducingHandler, // first request will get this
				streamHandler,         // request after reconnect will get this
			)
			streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
				p.startStream()
				<-p.requestsCh // first request
				select {
				case <-p.requestsCh: // got expected stream restart
					require.Fail(t, "got unexpected stream restart")
				case <-p.messageHandler.received:
					require.Fail(t, "got unexpected event")
				case <-time.After(time.Millisecond * 200):
					p.mockLog.AssertMessageMatch(t, true, ldlog.Error, "Invalid auto-configuration key")
				}
			})
		})
	}
}
