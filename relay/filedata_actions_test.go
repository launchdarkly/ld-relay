package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

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
		archiveManagerFactory: func(filename string, monitoringInterval time.Duration, handler filedata.UpdateHandler, loggers ldlog.Loggers) (
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
	return p.awaitClientFor(time.Second)
}

func (p offlineModeTestParams) awaitClientFor(duration time.Duration) testclient.CapturedLDClient {
	return helpers.RequireValue(p.t, p.clientsCreatedCh, duration, "timed out waiting for client creation")
}

func (p offlineModeTestParams) shouldNotCreateClient(timeout time.Duration) {
	if !helpers.AssertNoMoreValues(p.t, p.clientsCreatedCh, timeout, "unexpectedly created client") {
		p.t.FailNow()
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

		p.updateHandler.DeleteEnvironment(testFileDataEnv1.Params.EnvID, testFileDataEnv1.Params.Identifiers.FilterKey)

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

			require.Equal(t, 202, rr.Result().StatusCode)                   // event post was accepted
			helpers.AssertNoMoreValues(t, requestsCh, time.Millisecond*100) // nothing was forwarded to LD
		})
	})
}

func TestOfflineModeDeprecatedSDKKeyIsRespectedIfExpiryInFuture(t *testing.T) {
	// The goal here is to validate that if we load an offline mode archive containing a deprecated key,
	// it will be added as a credential (even though it was never previously seen as a primary key.) This situation
	// would happen when Relay is starting up at time T if the key was deprecated at a time before T.

	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {

		envData := RotateSDKKeyWithGracePeriod("primary-key", "deprecated-key", time.Now().Add(1*time.Hour))

		p.updateHandler.AddEnvironment(envData)

		client1 := p.awaitClient()
		assert.Equal(t, envData.Params.SDKKey, client1.Key)

		env := p.awaitEnvironment(testFileDataEnv1.Params.EnvID)

		assert.ElementsMatch(t, []credential.SDKCredential{envData.Params.SDKKey, envData.Params.EnvID}, env.GetCredentials())
		assert.ElementsMatch(t, []credential.SDKCredential{envData.Params.ExpiringSDKKey.Key}, env.GetDeprecatedCredentials())
	})
}

func TestOfflineModePrimarySDKKeyIsDeprecated(t *testing.T) {
	offlineModeTest(t, config.Config{}, func(p offlineModeTestParams) {
		update1 := RotateSDKKey("key1")

		p.updateHandler.AddEnvironment(update1)

		expectedClient1 := p.awaitClient()
		assert.Equal(t, update1.Params.SDKKey, expectedClient1.Key)

		env := p.awaitEnvironment(update1.Params.EnvID)

		assert.ElementsMatch(t, []credential.SDKCredential{update1.Params.SDKKey, update1.Params.EnvID}, env.GetCredentials())
		assert.Empty(t, env.GetDeprecatedCredentials())

		update2 := RotateSDKKeyWithGracePeriod("key2", "key1", time.Now().Add(1*time.Hour))
		p.updateHandler.UpdateEnvironment(update2)

		assert.ElementsMatch(t, []credential.SDKCredential{update2.Params.SDKKey, update1.Params.EnvID}, env.GetCredentials())
		assert.ElementsMatch(t, []credential.SDKCredential{update2.Params.ExpiringSDKKey.Key}, env.GetDeprecatedCredentials())

		update3 := RotateSDKKey("key3")
		p.updateHandler.UpdateEnvironment(update3)

		assert.ElementsMatch(t, []credential.SDKCredential{update3.Params.SDKKey, update1.Params.EnvID}, env.GetCredentials())

		// Note: key2 isn't in the deprecated list, because update3 was an immediate rotation (with no grace period for the
		// previous key.) At the same time, key1 is still deprecated until the hour is up.
		assert.ElementsMatch(t, []credential.SDKCredential{update2.Params.ExpiringSDKKey.Key}, env.GetDeprecatedCredentials())
	})
}

func TestOfflineModeSDKKeyCanExpire(t *testing.T) {
	// This test aims to deprecate an SDK key, sleep until the expiry, and then verify that the
	// key is no longer accepted.
	//
	// This test is extremely timing dependent, because we're unable to easily inject a mocked time
	// into the lower level components under test.

	// Instead, we configure the credential cleanup interval to be as short as possible (100ms)
	// and then sleep at least that amount of time after specifying a key expiry. The intention is to
	// simulate a real scenario, but fast enough for a test.

	const minimumCleanupInterval = 100 * time.Millisecond

	cfg := config.Config{}
	cfg.Main.ExpiredCredentialCleanupInterval = configtypes.NewOptDuration(minimumCleanupInterval)

	offlineModeTest(t, cfg, func(p offlineModeTestParams) {

		for i := 0; i < 3; i++ {
			primary := config.SDKKey(fmt.Sprintf("key%v", i+1))
			expiring := config.SDKKey(fmt.Sprintf("key%v", i))

			// It's important that the expiry be in the future (so that the key isn't ignored by the key rotator
			// component), but it should also be in the near future so the test doesn't need to sleep long.
			keyExpiry := time.Now().Add(10 * time.Millisecond)
			update1 := RotateSDKKeyWithGracePeriod(primary, expiring, keyExpiry)
			p.updateHandler.AddEnvironment(update1)

			// Waiting for the environment can take up to 1 second, but it could be much faster. In any case
			// we'll still need to sleep at least the cleanup interval to ensure the key is expired.
			env := p.awaitEnvironmentFor(update1.Params.EnvID, time.Second)
			assert.ElementsMatch(t, []credential.SDKCredential{update1.Params.SDKKey, update1.Params.EnvID}, env.GetCredentials())
			assert.ElementsMatch(t, []credential.SDKCredential{update1.Params.ExpiringSDKKey.Key}, env.GetDeprecatedCredentials())

			time.Sleep(minimumCleanupInterval)
			assert.ElementsMatch(t, []credential.SDKCredential{update1.Params.SDKKey, update1.Params.EnvID}, env.GetCredentials())
			assert.Empty(t, env.GetDeprecatedCredentials())
		}
	})
}
