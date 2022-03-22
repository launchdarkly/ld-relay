package relay

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/launchdarkly/go-test-helpers/v2/httphelpers"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/internal/filedata"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces/ldstoretypes"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents/ldstoreimpl"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file verify the offline mode behavior of Relay assuming that the low-level
// ArchiveManager implementation is working correctly. ArchiveManager itself is tested thoroughly,
// including error conditions and reconnection, in the filedata package where it is implemented.
// Rather than generating actual archive files, the tests here use a stub ArchiveManager that
// calls the same Relay methods that the real ArchiveManager would call if the file contained
// such-and-such data.

type foo struct{}

type offlineModeTestParams struct {
	relayTestHelper
	t                *testing.T
	relay            *Relay
	updateHandler    filedata.UpdateHandler
	clientsCreatedCh <-chan testclient.CapturedLDClient
	mockLog          *ldlogtest.MockLog
}

type stubArchiveManager struct{}

func (s stubArchiveManager) Close() error { return nil }

func offlineModeTest(
	t *testing.T,
	config config.Config,
	action func(p offlineModeTestParams),
) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	// In these tests, unlike most other tests, we use CapturedLDClient and real SDK client instances,
	// instead of FakeLDClient. That's because the implementation of offline mode requires the real SDK
	// client infrastructure, where the DataSource gets wired up to update the DataStore. We don't have
	// to worry about it making any calls to LaunchDarkly because offline mode always disables those.
	clientsCreatedCh := make(chan testclient.CapturedLDClient, 10)

	p := offlineModeTestParams{
		relayTestHelper:  relayTestHelper{t: t},
		t:                t,
		clientsCreatedCh: clientsCreatedCh,
		mockLog:          mockLog,
	}

	config.OfflineMode.FileDataSource = "filename is ignored in these tests"

	relay, err := newRelayInternal(config, relayInternalOptions{
		loggers:       mockLog.Loggers,
		clientFactory: testclient.RealLDClientFactoryWithChannel(true, clientsCreatedCh),
		archiveManagerFactory: func(filename string, handler filedata.UpdateHandler, loggers ldlog.Loggers) (
			filedata.ArchiveManagerInterface, error) {
			p.updateHandler = handler
			return stubArchiveManager{}, nil
		},
	})
	if err != nil {
		panic(err)
	}

	p.relay = relay
	p.relayTestHelper.relay = relay
	defer relay.Close()
	action(p)
}

func (p offlineModeTestParams) awaitClient() testclient.CapturedLDClient {
	select {
	case c := <-p.clientsCreatedCh:
		return c
	case <-time.After(time.Second):
		require.Fail(p.t, "timed out waiting for client creation")
		return testclient.CapturedLDClient{}
	}
}

func (p offlineModeTestParams) shouldNotCreateClient(timeout time.Duration) {
	select {
	case <-p.clientsCreatedCh:
		require.Fail(p.t, "unexpectedly created client")
	case <-time.After(timeout):
		break
	}
}

func TestOfflineModeInit(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(testFileDataEnv1)
		p.updateHandler.AddEnvironment(testFileDataEnv2)

		client1 := p.awaitClient()
		client2 := p.awaitClient()
		assert.Equal(t, testFileDataEnv1.Params.SDKKey, client1.Key)
		assert.Equal(t, testFileDataEnv2.Params.SDKKey, client2.Key)

		env1 := p.awaitEnvironment(testFileDataEnv1.Params.EnvID)
		p.assertEnvLookup(env1, testFileDataEnv1.Params)
		assertEnvProps(t, testFileDataEnv1.Params, env1)

		flags1, _ := env1.GetStore().GetAll(ldstoreimpl.Features())
		assert.Equal(t, testFileDataEnv1.SDKData[0].Items, flags1)

		env2 := p.awaitEnvironment(testFileDataEnv2.Params.EnvID)
		p.assertEnvLookup(env2, testFileDataEnv2.Params)
		assertEnvProps(t, testFileDataEnv2.Params, env2)

		flags2, _ := env2.GetStore().GetAll(ldstoreimpl.Features())
		assert.Equal(t, testFileDataEnv2.SDKData[0].Items, flags2)
	})
}

func TestOfflineModeUpdateEnvironment(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(testFileDataEnv1)

		client := p.awaitClient()
		assert.Equal(t, testFileDataEnv1.Params.SDKKey, client.Key)

		env := p.awaitEnvironment(testFileDataEnv1.Params.EnvID)
		flags1, _ := env.GetStore().GetAll(ldstoreimpl.Features())
		assert.Equal(t, testFileDataEnv1.SDKData[0].Items, flags1)

		testFileDataEnv1a := testFileDataEnv1
		testFileDataEnv1a.Params.Identifiers.EnvName += "-modified"

		extraFlag := ldbuilders.NewFlagBuilder("extra-flag").Version(1).Build()
		newFlags := make([]ldstoretypes.KeyedItemDescriptor, len(testFileDataEnv1.SDKData[0].Items))
		copy(newFlags, testFileDataEnv1.SDKData[0].Items)
		newFlags = append(newFlags, ldstoretypes.KeyedItemDescriptor{Key: extraFlag.Key, Item: sharedtest.FlagDesc(extraFlag)})
		sort.Slice(newFlags, func(i, j int) bool { return newFlags[i].Key < newFlags[j].Key })
		testFileDataEnv1a.SDKData = []ldstoretypes.Collection{
			{
				Kind:  ldstoreimpl.Features(),
				Items: newFlags,
			},
		}

		p.updateHandler.UpdateEnvironment(testFileDataEnv1a)

		assert.Equal(t, testFileDataEnv1a.Params.Identifiers, env.GetIdentifiers())

		flags2, _ := env.GetStore().GetAll(ldstoreimpl.Features())
		sort.Slice(flags2, func(i, j int) bool { return flags2[i].Key < flags2[j].Key })
		assert.Equal(t, newFlags, flags2)
	})
}

func TestOfflineModeDeleteEnvironment(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		p.updateHandler.AddEnvironment(testFileDataEnv1)
		p.updateHandler.AddEnvironment(testFileDataEnv2)

		client1 := p.awaitClient()
		client2 := p.awaitClient()
		assert.Equal(t, testFileDataEnv1.Params.SDKKey, client1.Key)
		assert.Equal(t, testFileDataEnv2.Params.SDKKey, client2.Key)

		_ = p.awaitEnvironment(testFileDataEnv1.Params.EnvID)
		_ = p.awaitEnvironment(testFileDataEnv1.Params.EnvID)

		p.updateHandler.DeleteEnvironment(testFileDataEnv1.Params.EnvID)

		p.shouldNotHaveEnvironment(testFileDataEnv1.Params.EnvID, time.Second)
	})
}

func TestOfflineModeEventsAreAcceptedAndDiscardedIfSendEventsIsTrue(t *testing.T) {
	eventRecorderHandler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(eventRecorderHandler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.SendEvents = true
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		allConfig.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)

		offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
			p.updateHandler.AddEnvironment(testFileDataEnv1)
			_ = p.awaitClient()

			rr := httptest.NewRecorder()
			headers := make(http.Header)
			headers.Add("Content-Type", "application/json")
			headers.Add("Authorization", string(testFileDataEnv1.Params.SDKKey))
			body := `[{"kind":"identify","creationDate":1000,"key":"userkey","user":{"key":"userkey"}}]`
			req := sharedtest.BuildRequest("POST", server.URL+"/bulk", []byte(body), headers)
			p.relay.Handler.ServeHTTP(rr, req)

			require.Equal(t, 202, rr.Result().StatusCode)                        // event post was accepted
			sharedtest.ExpectNoTestRequests(t, requestsCh, time.Millisecond*100) // nothing was forwarded to LD
		})
	})
}
