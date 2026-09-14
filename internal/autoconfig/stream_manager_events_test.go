package autoconfig

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/launchdarkly/ld-relay/v9/config"
)

func TestEnvironmentPutEvent(t *testing.T) {
	t.Run("add all new environments to empty state", func(t *testing.T) {
		event := makeEnvPutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			msg1 := p.requireMessage()
			require.NotNil(t, msg1.add)
			msg2 := p.requireMessage()
			require.NotNil(t, msg2.add)
			p.requireReceivedAllMessage()
			if msg1.add.EnvID == testEnv2.EnvID {
				msg1, msg2 = msg2, msg1
			}
			assert.Equal(t, testEnv1.ToParams(), *msg1.add)
			assert.Equal(t, testEnv2.ToParams(), *msg2.add)

			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "received configuration"))
			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "added item"))
			assert.Empty(t, p.mockLog.Messages(slog.LevelWarn))
			assert.Empty(t, p.mockLog.Messages(slog.LevelError))
		})
	})

	t.Run("add environment to previous environments", func(t *testing.T) {
		event := makeEnvPutEvent(testEnv1)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			msg1 := p.requireMessage()
			require.NotNil(t, msg1.add)
			assert.Equal(t, testEnv1.ToParams(), *msg1.add)
			p.requireReceivedAllMessage()

			p.stream.Enqueue(makeEnvPutEvent(testEnv1, testEnv2))
			msg2 := p.requireMessage()
			require.NotNil(t, msg2.add)
			assert.Equal(t, testEnv2.ToParams(), *msg2.add)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "added item"))
			assert.Empty(t, p.mockLog.Messages(slog.LevelWarn))
			assert.Empty(t, p.mockLog.Messages(slog.LevelError))
		})
	})

	t.Run("update environment from previous environments", func(t *testing.T) {
		event := makeEnvPutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = "newmobkey"
			testEnv1Mod.Version++

			p.stream.Enqueue(makeEnvPutEvent(testEnv1Mod, testEnv2))
			msg := p.requireMessage()
			require.NotNil(t, msg.update)
			assert.Equal(t, testEnv1Mod.ToParams(), *msg.update)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "properties have changed"))
			assert.Empty(t, p.mockLog.Messages(slog.LevelWarn))
			assert.Empty(t, p.mockLog.Messages(slog.LevelError))
		})
	})

	t.Run("update is ignored due to version number", func(t *testing.T) {
		event := makeEnvPutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = "newmobkey"

			p.stream.Enqueue(makeEnvPutEvent(testEnv1Mod, testEnv2))
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring out-of-order update"))
			assert.Empty(t, p.mockLog.Messages(slog.LevelWarn))
			assert.Empty(t, p.mockLog.Messages(slog.LevelError))
		})
	})

	t.Run("delete environment from previous environments", func(t *testing.T) {
		event := makeEnvPutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			p.stream.Enqueue(makeEnvPutEvent(testEnv2))
			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "removed item"))
			assert.Empty(t, p.mockLog.Messages(slog.LevelWarn))
			assert.Empty(t, p.mockLog.Messages(slog.LevelError))
		})
	})

	t.Run("unrecognized path", func(t *testing.T) {
		json := `{"path": "/elsewhere","data": {}}`
		event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			p.requireNoMoreMessages()
			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "ignoring event for unknown path"))
		})
	})

	t.Run("env rep has ID that doesn't match key", func(t *testing.T) {
		json := `{"path": "/","data": {"environments": {"wrongkey":{"envId":"other"},"` +
			string(testEnv1.EnvID) + `":` + toJSON(testEnv1) + `}}}`
		event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			msg := p.requireMessage()
			require.NotNil(t, msg.add)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()
			assert.True(t, p.mockLog.HasMessage(slog.LevelWarn, "ignoring environment data whose envId"))
		})
	})
}

func TestEnvironmentPatchEvent(t *testing.T) {
	t.Run("new environment", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchEnvEvent(testEnv1))

			msg := p.requireMessage()
			require.NotNil(t, msg.add)
			assert.Equal(t, testEnv1.ToParams(), *msg.add)
		})
	})

	t.Run("updated environment", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchEnvEvent(testEnv1))

			_ = p.requireMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")
			testEnv1Mod.Version++

			p.stream.Enqueue(makePatchEnvEvent(testEnv1Mod))

			msg := p.requireMessage()
			require.NotNil(t, msg.update)
			assert.Equal(t, testEnv1Mod.ToParams(), *msg.update)
		})
	})

	t.Run("update is ignored due to version number", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchEnvEvent(testEnv1))

			_ = p.requireMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")

			p.stream.Enqueue(makePatchEnvEvent(testEnv1Mod))

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring out-of-order update"))
		})
	})

	t.Run("out-of-order update after delete is ignored", func(t *testing.T) {
		initEvent := makeEnvPutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)

			staleEvent := makePatchEnvEvent(testEnv1)
			p.stream.Enqueue(staleEvent)

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring out-of-order update"))
		})
	})

	t.Run("update with higher version after delete is a valid add", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()

			event := makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version)
			p.stream.Enqueue(event)

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")
			testEnv1Mod.Version++

			p.stream.Enqueue(makePatchEnvEvent(testEnv1Mod))

			msg := p.requireMessage()
			require.NotNil(t, msg.add)
			assert.Equal(t, testEnv1Mod.ToParams(), *msg.add)
		})
	})

	t.Run("unrecognized path results in debug-level log", func(t *testing.T) {
		json := `{"path": "/otherthings","data": {}}`
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()
			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring unknown entity"))
		})
	})

	t.Run("env rep has ID that doesn't match path key", func(t *testing.T) {
		json := `{"path": "/environments/wrongkey","data":` + toJSON(testEnv1) + `}`
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()
			assert.True(t, p.mockLog.HasMessage(slog.LevelWarn, "ignoring environment data"))
		})
	})
}

func TestEnvironmentDeleteEvent(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		initEvent := makeEnvPutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)
		})
	})

	t.Run("delete is ignored due to version number", func(t *testing.T) {
		initEvent := makeEnvPutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version)
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring out-of-order delete"))
		})
	})

	t.Run("delete is ignored because it's already deleted", func(t *testing.T) {
		initEvent := makeEnvPutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)

			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring out-of-order delete"))
		})
	})

	t.Run("unknown environment", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()

			event := makeDeleteEnvEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()
		})
	})

	t.Run("unrecognized path", func(t *testing.T) {
		json := `{"path": "/otherthings"}`
		event := httphelpers.SSEEvent{Event: DeleteEvent, Data: json}
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "ignoring unknown entity"))
		})
	})
}

func TestFilterEventsAreIgnored(t *testing.T) {
	// Payload filters are not supported, so filter entities are no longer recognized. LaunchDarkly
	// still sends them, so each shape must be ignored: no handler call, and no stream restart. The
	// path dispatch treats them like any other unknown entity.
	//
	// "Nothing happened" on its own would also hold for a stream that died, so each case ends by
	// sending an environment patch and requiring it through. That proves the connection survived
	// the filter event rather than merely going quiet.
	for name, event := range map[string]httphelpers.SSEEvent{
		"patch":           {Event: PatchEvent, Data: `{"path": "/filters/filterid1", "data": {"projKey": "p", "key": "k", "version": 1}}`},
		"delete":          {Event: DeleteEvent, Data: `{"path": "/filters/filterid1", "version": 2}`},
		"malformed patch": {Event: PatchEvent, Data: `{"path": "/filters/filterid1", "data": 999}`},
	} {
		t.Run(name, func(t *testing.T) {
			streamManagerTest(t, nil, func(p streamManagerTestParams) {
				p.startStream()
				<-p.requestsCh
				p.stream.Enqueue(event)

				select {
				case msg := <-p.messageHandler.received:
					require.Failf(t, "filter event reached the handler", "message: %+v", msg)
				case <-p.requestsCh:
					require.Fail(t, "filter event restarted the stream")
				case <-time.After(time.Millisecond * 200):
					// Nothing happened, which is expected. The liveness check follows.
				}

				p.stream.Enqueue(makePatchEnvEvent(testEnv1))
				msg := p.requireMessage()
				require.NotNil(t, msg.add, "stream stopped serving environments after a filter event")
				assert.Equal(t, testEnv1.ToParams(), *msg.add)
			})
		})
	}
}

func TestReconnectEvent(t *testing.T) {
	streamManagerTest(t, nil, func(p streamManagerTestParams) {
		p.startStream()
		<-p.requestsCh

		p.stream.Enqueue(httphelpers.SSEEvent{Event: "reconnect", Data: " "})

		select {
		case <-p.messageHandler.received:
			require.Fail(t, "received unexpected message")
		case <-p.requestsCh: // got expected stream restart
			assert.True(t, p.mockLog.HasMessage(slog.LevelInfo, "will restart auto-configuration stream"))
		case <-time.After(time.Second):
			require.Fail(t, "timed out waiting for stream restart")
		}
	})
}

func TestUnknownEventIsIgnored(t *testing.T) {
	event := httphelpers.SSEEvent{Event: "magic", Data: "{}"}
	streamManagerTest(t, &event, func(p streamManagerTestParams) {
		p.startStream()

		p.requireNoMoreMessages()
		assert.True(t, p.mockLog.HasMessage(slog.LevelWarn, "ignoring unrecognized stream event"))
		assert.True(t, p.mockLog.HasMessage(slog.LevelDebug, "received SSE event"))
	})
}
