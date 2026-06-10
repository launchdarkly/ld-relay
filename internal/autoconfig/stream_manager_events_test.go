package autoconfig

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/launchdarkly/ld-relay/v8/config"
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Received configuration for 2")
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment "+string(testEnv1.EnvID))
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment "+string(testEnv2.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment "+string(testEnv2.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Properties have changed for environment "+string(testEnv1.EnvID))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order update")
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order update")
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order update")
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
			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring unknown entity")
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order delete")
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order delete")
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

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring unknown entity")
		})
	})
}

func TestFilterPutEvent(t *testing.T) {
	t.Run("add all new environments and filters to empty state", func(t *testing.T) {
		event := makeEnvFilterPutEvent(
			[]envfactory.EnvironmentRep{testEnv1, testEnv2},
			[]envfactory.FilterRep{testFilter1, testFilter2},
		)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			msg1 := p.requireMessage()
			require.NotNil(t, msg1.add)
			msg2 := p.requireMessage()
			require.NotNil(t, msg2.add)
			msg3 := p.requireMessage()
			require.NotNil(t, msg3.addFilter)
			msg4 := p.requireMessage()
			require.NotNil(t, msg4.addFilter)

			p.requireReceivedAllMessage()

			assert.ElementsMatch(t,
				[]envfactory.EnvironmentParams{testEnv1.ToParams(), testEnv2.ToParams()},
				[]envfactory.EnvironmentParams{*msg1.add, *msg2.add},
			)

			assert.ElementsMatch(t,
				[]envfactory.FilterParams{
					testFilter1.ToTestParams(),
					testFilter2.ToTestParams()},
				[]envfactory.FilterParams{
					*msg3.addFilter,
					*msg4.addFilter,
				},
			)

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Received configuration for 2")
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment ")
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added environment ")
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added filter ")
			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added filter ")

			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("add filter to previous filters", func(t *testing.T) {
		event := makeFilterPutEvent(testFilter1)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			msg1 := p.requireMessage()
			require.NotNil(t, msg1.addFilter)
			assert.Equal(t, testFilter1.ToTestParams(), *msg1.addFilter)
			p.requireReceivedAllMessage()

			p.stream.Enqueue(makeFilterPutEvent(testFilter1, testFilter2))
			msg2 := p.requireMessage()
			require.NotNil(t, msg2.addFilter)
			assert.Equal(t, testFilter2.ToTestParams(), *msg2.addFilter)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Added filter "+string(testFilter2.FilterKey))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})

	t.Run("delete environment from previous environments", func(t *testing.T) {
		event := makeFilterPutEvent(testFilter1, testFilter2)
		streamManagerTest(t, &event, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			p.stream.Enqueue(makeFilterPutEvent(testFilter2))
			msg := p.requireMessage()
			require.NotNil(t, msg.deleteFilter)
			assert.Equal(t, testFilter1.ToTestParams().ID, *msg.deleteFilter)
			p.requireReceivedAllMessage()

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info, "Removed filter "+string(testFilter1.FilterKey))
			assert.Len(t, p.mockLog.GetOutput(ldlog.Warn), 0)
			assert.Len(t, p.mockLog.GetOutput(ldlog.Error), 0)
		})
	})
}

func TestFilterPatchEvent(t *testing.T) {
	t.Run("new filter", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()
			p.stream.Enqueue(makePatchFilterEvent(testFilter1))

			msg := p.requireMessage()
			require.NotNil(t, msg.addFilter)
			assert.Equal(t, testFilter1.ToTestParams(), *msg.addFilter)
		})
	})

	t.Run("out-of-order patch after delete is ignored", func(t *testing.T) {
		initEvent := makeFilterPutEvent(testFilter1)
		filter1ID := testFilter1.ToTestParams().ID

		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteFilterEvent(filter1ID, testFilter1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.deleteFilter)
			assert.Equal(t, filter1ID, *msg.deleteFilter)

			staleEvent := makePatchFilterEvent(testFilter1)
			p.stream.Enqueue(staleEvent)

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order update")
		})
	})

	t.Run("patch with higher version after delete is a valid add", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()

			filter1ID := testFilter1.ToTestParams().ID

			event := makeDeleteFilterEvent(filter1ID, testFilter1.Version)
			p.stream.Enqueue(event)

			testFilter1Mod := testFilter1
			testFilter1Mod.Version++

			p.stream.Enqueue(makePatchFilterEvent(testFilter1Mod))

			msg := p.requireMessage()
			require.NotNil(t, msg.addFilter)
			assert.Equal(t, testFilter1Mod.ToTestParams(), *msg.addFilter)
		})
	})
}

func TestFilterDeleteEvent(t *testing.T) {
	filter1ID := testFilter1.ToTestParams().ID

	t.Run("success", func(t *testing.T) {
		initEvent := makeFilterPutEvent(testFilter1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteFilterEvent(filter1ID, testFilter1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.deleteFilter)
			assert.Equal(t, filter1ID, *msg.deleteFilter)
		})
	})

	t.Run("delete is ignored due to version number", func(t *testing.T) {
		initEvent := makeFilterPutEvent(testFilter1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteFilterEvent(filter1ID, testFilter1.Version)
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order delete")
		})
	})

	t.Run("delete is ignored because it's already deleted", func(t *testing.T) {
		initEvent := makeFilterPutEvent(testFilter1)
		streamManagerTest(t, &initEvent, func(p streamManagerTestParams) {
			p.startStream()

			_ = p.requireMessage()
			p.requireReceivedAllMessage()

			event := makeDeleteFilterEvent(filter1ID, testFilter1.Version+1)
			p.stream.Enqueue(event)

			msg := p.requireMessage()
			require.NotNil(t, msg.deleteFilter)
			assert.Equal(t, filter1ID, *msg.deleteFilter)

			p.stream.Enqueue(event)

			p.requireNoMoreMessages()

			p.mockLog.AssertMessageMatch(t, true, ldlog.Debug, "Ignoring out-of-order delete")
		})
	})

	t.Run("unknown filter", func(t *testing.T) {
		streamManagerTest(t, nil, func(p streamManagerTestParams) {
			p.startStream()

			event := makeDeleteFilterEvent(filter1ID, testFilter1.Version+1)
			p.stream.Enqueue(event)

			p.requireNoMoreMessages()
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
