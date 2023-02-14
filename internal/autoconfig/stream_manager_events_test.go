package autoconfig

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/launchdarkly/ld-relay/v8/config"
)

func TestPutEvent(t *testing.T) {
	t.Run("add all new environments to empty state", func(t *testing.T) {
		event := makePutEvent(testEnv1, testEnv2)
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Received configuration for 2")
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment "+string(testEnv1.EnvID))
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment "+string(testEnv2.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("add environment to previous environments", func(t *testing.T) {
		event := makePutEvent(testEnv1)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			msg1 := p.requireMessage()
			require.NotNil(t, msg1.add)
			assert.Equal(t, testEnv1.ToParams(), *msg1.add)
			p.requireReceivedAllMessage()

			p.stream.Enqueue(makePutEvent(testEnv1, testEnv2))
			msg2 := p.requireMessage()
			require.NotNil(t, msg2.add)
			assert.Equal(t, testEnv2.ToParams(), *msg2.add)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment "+string(testEnv2.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("update environment from previous environments", func(t *testing.T) {
		event := makePutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")
			testEnv1Mod.Version++

			p.stream.Enqueue(makePutEvent(testEnv1Mod, testEnv2))
			msg := p.requireMessage()
			require.NotNil(t, msg.update)
			assert.Equal(t, testEnv1Mod.ToParams(), *msg.update)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Properties have changed for environment "+string(testEnv1.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("update is ignored due to version number", func(t *testing.T) {
		event := makePutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")

			p.stream.Enqueue(makePutEvent(testEnv1Mod, testEnv2))
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Ignoring out-of-order update")
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("delete environment from previous environments", func(t *testing.T) {
		event := makePutEvent(testEnv1, testEnv2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			p.stream.Enqueue(makePutEvent(testEnv2))
			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Removed environment "+string(testEnv1.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("unrecognized path", func(t *testing.T) {
		json := `{"path": "/elsewhere","data": {}}`
		event := httphelpers.SSEEvent{Event: PutEvent, Data: json}
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			p.requireNoMoreMessages()
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, `Ignoring "put" event for unknown path`)
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
			p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Ignoring environment data whose envId")
		})
	})
}

func TestPatchEvent(t *testing.T) {
	t.Run("new environment", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchEvent(testEnv1))

			msg := p.requireMessage()
			require.NotNil(t, msg.add)
			assert.Equal(t, testEnv1.ToParams(), *msg.add)
		})
	})

	t.Run("updated environment", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchEvent(testEnv1))

			_ = p.requireMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")
			testEnv1Mod.Version++

			p.stream.Enqueue(makePatchEvent(testEnv1Mod))

			msg := p.requireMessage()
			require.NotNil(t, msg.update)
			assert.Equal(t, testEnv1Mod.ToParams(), *msg.update)
		})
	})

	t.Run("update is ignored due to version number", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchEvent(testEnv1))

			_ = p.requireMessage()

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")

			p.stream.Enqueue(makePatchEvent(testEnv1Mod))

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Ignoring out-of-order update")
		})
	})

	t.Run("out-of-order update after delete is ignored", func(t *testing.T) {
		initEvent := makePutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)

			staleEvent := makePatchEvent(testEnv1)
			p.stream.Enqueue(staleEvent)

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Ignoring out-of-order update")
		})
	})

	t.Run("update with higher version after delete is a valid add", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()

			event := makeDeleteEvent(testEnv1.EnvID, testEnv1.Version)
			p.stream.Enqueue(event)

			testEnv1Mod := testEnv1
			testEnv1Mod.MobKey = config.MobileKey("newmobkey")
			testEnv1Mod.Version++

			p.stream.Enqueue(makePatchEvent(testEnv1Mod))

			msg := p.requireMessage()
			require.NotNil(t, msg.add)
			assert.Equal(t, testEnv1Mod.ToParams(), *msg.add)
		})
	})

	t.Run("unrecognized path", func(t *testing.T) {
		json := `{"path": "/otherthings","data": {}}`
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, `Ignoring "patch" event for unknown path`)
		})
	})

	t.Run("env rep has ID that doesn't match path key", func(t *testing.T) {
		json := `{"path": "/environments/wrongkey","data":` + toJSON(testEnv1) + `}`
		event := httphelpers.SSEEvent{Event: PatchEvent, Data: json}
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()
			p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Ignoring environment data")
		})
	})
}

func TestDeleteEvent(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		initEvent := makePutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)
		})
	})

	t.Run("delete is ignored due to version number", func(t *testing.T) {
		initEvent := makePutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEvent(testEnv1.EnvID, testEnv1.Version)
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Ignoring out-of-order delete")
		})
	})

	t.Run("delete is ignored because it's already deleted", func(t *testing.T) {
		initEvent := makePutEvent(testEnv1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteEvent(testEnv1.EnvID, testEnv1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.delete)
			assert.Equal(t, testEnv1.EnvID, *msg.delete)

			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			// TODO(cwaldren): This used to have shouldMatch: false, because the expectation was that
			// there wouldn't be a log message if an out-of-order delete arrived for an *already deleted*
			// environment. I think it's debatable whether that is a good thing - perhaps it happens a lot or something?
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Ignoring out-of-order delete")
		})
	})

	t.Run("unknown environment", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()

			event := makeDeleteEvent(testEnv1.EnvID, testEnv1.Version+1)
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, `Ignoring "delete" event for unknown path`)
		})
	})
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
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Will restart auto-configuration stream")
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
		p.mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Ignoring unrecognized stream event")
		p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, `Received "magic" event: {}`)
	})
}
