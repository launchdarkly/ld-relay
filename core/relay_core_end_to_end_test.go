package core

import (
	"net/http/httptest"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/go-test-helpers/v2/ldservices"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayCoreEndToEnd(t *testing.T) {
	coreMockLog := ldlogtest.NewMockLog()
	sdkMockLog := ldlogtest.NewMockLog()
	defer st.DumpLogIfTestFailed(t, coreMockLog)
	defer st.DumpLogIfTestFailed(t, sdkMockLog)

	flagKey := "test-flag"
	putEvent := ldservices.NewServerSDKData().Flags(ldservices.FlagOrSegment(flagKey, 1)).ToPutEvent()
	streamHandler, _ := ldservices.ServerSideStreamingServiceHandler(putEvent)
	serverHandler, requestsCh := httphelpers.RecordingHandler(streamHandler)

	httphelpers.WithServer(serverHandler, func(streamServer *httptest.Server) {
		optStreamURI, _ := configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
		config := c.Config{
			Main: c.MainConfig{
				StreamURI: optStreamURI,
			},
			Environment: st.MakeEnvConfigs(st.EnvMain),
		}
		core, err := NewRelayCore(
			config,
			coreMockLog.Loggers,
			nil,
			"1.2.3",
			"FakeRelay",
			relayenv.LogNameIsEnvID,
		)
		require.NoError(t, err)
		defer core.Close()

		streamReq := <-requestsCh
		assert.Equal(t, string(st.EnvMain.Config.SDKKey), streamReq.Request.Header.Get("Authorization"))

		httphelpers.WithServer(core.MakeRouter(), func(relayServer *httptest.Server) {
			sdkConfig := ld.Config{
				DataSource: ldcomponents.StreamingDataSource().BaseURI(relayServer.URL),
				Events:     ldcomponents.NoEvents(),
				Logging:    ldcomponents.Logging().Loggers(sdkMockLog.Loggers),
			}
			client, err := ld.MakeCustomClient(string(st.EnvMain.Config.SDKKey), sdkConfig, 5*time.Second)
			require.NoError(t, err)
			defer client.Close()
			flags := client.AllFlagsState(lduser.NewUser("user-key"))
			assert.Equal(t, map[string]ldvalue.Value{flagKey: ldvalue.Null()}, flags.ToValuesMap())
		})
	})
}
