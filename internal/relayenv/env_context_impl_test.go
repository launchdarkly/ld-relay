package relayenv

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"
	"github.com/launchdarkly/ld-relay/v8/internal/util"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/bigsegments"
	"github.com/launchdarkly/ld-relay/v8/internal/events"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/metrics"
	"github.com/launchdarkly/ld-relay/v8/internal/sdks"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v8/internal/streams"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	ldevents "github.com/launchdarkly/go-sdk-events/v3"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"
)

const envName = "envname"

func requireEnvReady(t *testing.T, readyCh <-chan EnvContext) EnvContext {
	return helpers.RequireValue(t, readyCh, time.Second, "timed out waiting for environment")
}

func requireClientReady(t *testing.T, clientCh chan *testclient.FakeLDClient) *testclient.FakeLDClient {
	return helpers.RequireValue(t, clientCh, time.Second, "timed out waiting for client")
}

func makeBasicEnv(t *testing.T, envConfig config.EnvConfig, clientFactory sdks.ClientFactoryFunc,
	loggers ldlog.Loggers, readyCh chan EnvContext) EnvContext {
	return makeBasicEnvWithMapper(t, envConfig, clientFactory, loggers, readyCh, mockConnectionMapper{})
}

func makeBasicEnvWithMapper(t *testing.T, envConfig config.EnvConfig, clientFactory sdks.ClientFactoryFunc,
	loggers ldlog.Loggers, readyCh chan EnvContext, connMapper ConnectionMapper) EnvContext {
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:      EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:        envConfig,
		ClientFactory:    clientFactory,
		Loggers:          loggers,
		ConnectionMapper: connMapper,
	}, readyCh)
	require.NoError(t, err)
	return env
}

type mockConnectionMapper struct {
}

func (m mockConnectionMapper) AddConnectionMapping(scopedCredential sdkauth.ScopedCredential, envContext EnvContext) {

}
func (m mockConnectionMapper) RemoveConnectionMapping(scopedCredential sdkauth.ScopedCredential) {

}

// recordingConnectionMapper tracks which scoped credentials currently have a connection mapping, so a
// test can assert that a credential's mapping survived (or was torn down).
type recordingConnectionMapper struct {
	mu     sync.Mutex
	mapped map[sdkauth.ScopedCredential]bool
}

func (m *recordingConnectionMapper) AddConnectionMapping(scopedCredential sdkauth.ScopedCredential, _ EnvContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mapped == nil {
		m.mapped = map[sdkauth.ScopedCredential]bool{}
	}
	m.mapped[scopedCredential] = true
}

func (m *recordingConnectionMapper) RemoveConnectionMapping(scopedCredential sdkauth.ScopedCredential) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mapped, scopedCredential)
}

func (m *recordingConnectionMapper) isMapped(filterKey config.FilterKey, cred credential.SDKCredential) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mapped[sdkauth.NewScoped(filterKey, cred)]
}

func TestConstructorBasicProperties(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, envName, env.GetIdentifiers().ConfiguredName)
	assert.Equal(t, time.Hour, env.GetTTL())
	assert.True(t, env.IsSecureMode())
	assert.Nil(t, env.GetEventDispatcher())                        // events were not enabled
	assert.Equal(t, context.Background(), env.GetMetricsContext()) // metrics aren't being used

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, envConfig.MobileKey)
	assert.Contains(t, creds, envConfig.EnvID)

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, env.GetClient(), requireClientReady(t, clientCh))
	assert.Nil(t, env.GetInitError())

	assert.NotNil(t, env.GetStore())
}

func TestConstructorWithOnlySDKKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, env.GetClient(), requireClientReady(t, clientCh))
	assert.Nil(t, env.GetInitError())
}

func TestConstructorWithJSClientContext(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	jsClientContext := JSClientContext{Origins: []string{"origin"}}
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:     EnvIdentifiers{ConfiguredName: envName},
		EnvConfig:       envConfig,
		ClientFactory:   testclient.FakeLDClientFactory(true),
		JSClientContext: jsClientContext,
		Loggers:         ldlog.NewDisabledLoggers(),
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	assert.Equal(t, jsClientContext, env.GetJSClientContext())
}

func TestLogPrefix(t *testing.T) {
	testPrefix := func(desc string, mode LogNameMode, sdkKey config.SDKKey, envID config.EnvironmentID, expected string) {
		t.Run(desc, func(t *testing.T) {
			envConfig := config.EnvConfig{SDKKey: sdkKey, EnvID: envID}
			mockLog := ldlogtest.NewMockLog()
			env, err := NewEnvContext(EnvContextImplParams{
				Identifiers:   EnvIdentifiers{ConfiguredName: "name"},
				EnvConfig:     envConfig,
				ClientFactory: testclient.FakeLDClientFactory(true),
				UserAgent:     "user-agent",
				LogNameMode:   mode,
				Loggers:       mockLog.Loggers,
			}, nil)
			require.NoError(t, err)
			defer env.Close()
			envImpl := env.(*envContextImpl)
			envImpl.loggers.Error("message")
			mockLog.AssertMessageMatch(t, true, ldlog.Error, "^"+regexp.QuoteMeta(expected)+" message")
		})
	}

	testPrefix("SDK key", LogNameIsSDKKey, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...7890]")
	testPrefix("env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("abcdefghij"), "[env: ...ghij]")
	testPrefix("env ID not set", LogNameIsEnvID, config.SDKKey("1234567890"), "", "[env: ...7890]")
	testPrefix("impossibly short SDK key", LogNameIsSDKKey, config.SDKKey("890"), config.EnvironmentID("abcdefghij"), "[env: 890]")
	testPrefix("impossibly short env ID", LogNameIsEnvID, config.SDKKey("1234567890"), config.EnvironmentID("hij"), "[env: hij]")
}

// mustBuildAcceptedSet builds the set from b, failing the test if Build returns an error.
func mustBuildAcceptedSet(t *testing.T, b *credential.AcceptedSetBuilder) credential.AcceptedSet {
	t.Helper()
	set, err := b.Build()
	require.NoError(t, err)
	return set
}

func TestAddRemoveCredential(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, nil)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	mobileKey := st.EnvWithAllCredentials.Config.MobileKey
	envID := st.EnvWithAllCredentials.Config.EnvID

	// Reconcile to the full set: the SDK key (anchor) plus a mobile key and an environment ID.
	env.ReconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).WithPrimaryMobileKey(credential.MobileKeyParams{Value: mobileKey}).WithEnvironmentID(envID)))

	creds := env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, mobileKey)
	assert.Contains(t, creds, envID)

	// Reconciling with a different mobile key evicts the previous one.
	newMobileKey := config.MobileKey("evict-the-previous-key")
	env.ReconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).WithPrimaryMobileKey(credential.MobileKeyParams{Value: newMobileKey}).WithEnvironmentID(envID)))

	creds = env.GetCredentials()
	assert.Len(t, creds, 3)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.NotContains(t, creds, mobileKey)
	assert.Contains(t, creds, newMobileKey)
	assert.Contains(t, creds, envID)
}

func TestAddExistingCredentialDoesNothing(t *testing.T) {
	envConfig := st.EnvMain.Config

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, nil)
	defer env.Close()

	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())

	mobileKey := st.EnvWithAllCredentials.Config.MobileKey
	set := mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).WithPrimaryMobileKey(credential.MobileKeyParams{Value: mobileKey}))

	env.ReconcileCredentials(set)

	creds := env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, mobileKey)

	// Reconciling with the same set again changes nothing.
	env.ReconcileCredentials(set)

	creds = env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, mobileKey)
}

func TestChangeSDKKey(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	key2 := config.SDKKey("key2")

	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	client1 := requireClientReady(t, clientCh)
	assert.Equal(t, env.GetClient(), client1)
	assert.Nil(t, env.GetInitError())

	// The environment should have been initialized with a single SDK key (found in the envConfig.)
	// At this point, there's no deprecated credentials.
	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetCredentials())
	assert.Empty(t, env.GetDeprecatedCredentials())

	// For the purposes of key rotation, we'll make time deterministic. We build an AcceptedSet
	// with key2 as anchor and envConfig.SDKKey expiring in one hour, then drive the time-injectable
	// reconcileCredentials. The cleanup ticker path (triggerCredentialChanges) is exercised below.
	start := time.Unix(1000, 0)

	// Upon rotating to key2, the original key should still be valid for an hour.
	rotationSet, err := credential.NewAcceptedSetBuilder().
		WithAnchor(credential.SDKKeyParams{Value: key2}).
		WithSDKKey(credential.SDKKeyParams{Value: envConfig.SDKKey, Expiry: util.PtrOrNil(start.Add(1 * time.Hour))}).
		Build()
	require.NoError(t, err)
	envImpl.reconcileCredentials(rotationSet, start)

	// In the new accepted-set model both keys are accepted (GetCredentials includes both) until
	// the old key's expiry elapses. GetDeprecatedCredentials reports the expiring accepted key so
	// callers like the status endpoint can surface it without distinguishing the rotation path.
	creds := env.GetCredentials()
	assert.Len(t, creds, 2)
	assert.Contains(t, creds, key2)
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Equal(t, []credential.SDKCredential{envConfig.SDKKey}, env.GetDeprecatedCredentials())

	client2 := requireClientReady(t, clientCh)
	assert.NotEqual(t, client1, client2)
	// requireClientReady fires when the factory sends on clientCh, but startSDKClient stores
	// the client in c.clients after that send. Use Eventually to wait for the map store.
	require.Eventually(t, func() bool {
		return env.GetClient() == client2
	}, time.Second, 10*time.Millisecond, "env.GetClient() should return client2 after rotation")

	// The client for the original SDK key should not have been closed, since it's valid for an hour.
	if !helpers.AssertChannelNotClosed(t, client1.CloseCh, 1*time.Second, "client for envConfig.SDKKey should not have been closed yet") {
		t.FailNow()
	}

	// Simulate an amount of time passing that is less than the expiry window. The original key should still be valid.
	envImpl.triggerCredentialChanges(start.Add(45 * time.Minute))
	if !helpers.AssertChannelNotClosed(t, client1.CloseCh, 1*time.Second, "client for envConfig.SDKKey should not have been closed yet") {
		t.FailNow()
	}

	// We are now an instant after the expiry. This should cause the original key to be removed
	// and trigger its client to close.
	envImpl.triggerCredentialChanges(start.Add(1*time.Hour + 1*time.Millisecond))
	assert.Equal(t, []credential.SDKCredential{key2}, env.GetCredentials())
	assert.Empty(t, env.GetDeprecatedCredentials())

	if !helpers.AssertChannelClosed(t, client1.CloseCh, 1*time.Second, "client for envConfig.SDKKey should have been closed") {
		t.FailNow()
	}

}

// TestMobileKeyReconcileExpiry drives a mobile key carrying a per-key expiry end-to-end through the
// reconcile path: ReconcileCredentials records the expiry as data on the accepted entry, and the
// cleanup ticker (triggerCredentialChanges → StepTime) later evicts the key once its expiry elapses.
func TestMobileKeyReconcileExpiry(t *testing.T) {
	envConfig := st.EnvMobile.Config
	readyCh := make(chan EnvContext, 1)

	primaryMobile := envConfig.MobileKey
	expiringMobile := config.MobileKey("mob-expiring")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.FakeLDClientFactory(true), mockLog.Loggers, readyCh)
	defer env.Close()
	envImpl := env.(*envContextImpl)

	assert.Equal(t, env, requireEnvReady(t, readyCh))

	start := time.Unix(2000, 0)
	expiry := start.Add(1 * time.Hour)

	// Reconcile to a set that accepts the primary mobile key (permanent) plus a second mobile key that
	// carries a per-key expiry.
	envImpl.reconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().
			WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
			WithPrimaryMobileKey(credential.MobileKeyParams{Value: primaryMobile}).
			WithMobileKey(credential.MobileKeyParams{Value: expiringMobile, Expiry: util.PtrOrNil(expiry)})),
		start)

	// Reconcile stores the expiry as data, so before it elapses the key is accepted (not deprecated).
	assert.Contains(t, env.GetCredentials(), expiringMobile)
	assert.NotContains(t, env.GetDeprecatedCredentials(), expiringMobile)

	// Halfway through, still accepted.
	envImpl.triggerCredentialChanges(start.Add(30 * time.Minute))
	assert.Contains(t, env.GetCredentials(), expiringMobile)

	// One moment past expiry: the cleanup ticker evicts the expiring mobile key; the primary survives.
	envImpl.triggerCredentialChanges(expiry.Add(1 * time.Millisecond))
	assert.NotContains(t, env.GetCredentials(), expiringMobile)
	assert.Contains(t, env.GetCredentials(), primaryMobile)
}

func TestNonAnchorSDKKeysDoNotOpenUpstreamClient(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	// Buffer large enough to catch any unexpected extra clients.
	clientCh := make(chan *testclient.FakeLDClient, 10)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	// One client is opened for the anchor key during construction.
	assert.Equal(t, env, requireEnvReady(t, readyCh))
	anchorClient := requireClientReady(t, clientCh)
	assert.Equal(t, envConfig.SDKKey, anchorClient.Key)

	nonAnchorKey1 := config.SDKKey("non-anchor-key-1")
	nonAnchorKey2 := config.SDKKey("non-anchor-key-2")

	// Reconcile to anchor + 2 non-anchor SDK keys. The anchor is unchanged, so no new anchor client
	// is needed. Non-anchor keys must get envStreams + handlers + connection mapping but must NOT
	// open an upstream client.
	env.ReconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().
			WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
			WithSDKKey(credential.SDKKeyParams{Value: nonAnchorKey1}).
			WithSDKKey(credential.SDKKeyParams{Value: nonAnchorKey2})))

	// All three SDK keys are accepted...
	creds := env.GetCredentials()
	assert.Contains(t, creds, envConfig.SDKKey)
	assert.Contains(t, creds, nonAnchorKey1)
	assert.Contains(t, creds, nonAnchorKey2)

	// ...but no additional upstream client was started.
	if !helpers.AssertNoMoreValues(t, clientCh, 200*time.Millisecond) {
		t.FailNow()
	}
}

// TestGetClientReturnsAnchorInMultiKeyEnv verifies that GetClient returns the anchor's upstream
// client when the environment holds multiple SDK keys. Non-anchor SDK keys share the same
// upstream connection (the anchor's), so GetClient must never return a non-anchor client
// and must remain non-nil after non-anchor keys are added. This is the contract callers of
// GetClient depend on: nil means "env not ready"; non-nil means "use this client."
func TestGetClientReturnsAnchorInMultiKeyEnv(t *testing.T) {
	envConfig := st.EnvMain.Config
	readyCh := make(chan EnvContext, 1)
	clientCh := make(chan *testclient.FakeLDClient, 10)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	anchorClient := requireClientReady(t, clientCh)
	assert.Equal(t, envConfig.SDKKey, anchorClient.Key)

	// GetClient must return the anchor's client even before any non-anchor keys are added.
	assert.Equal(t, anchorClient, env.GetClient())

	nonAnchorKey1 := config.SDKKey("non-anchor-key-1")
	nonAnchorKey2 := config.SDKKey("non-anchor-key-2")

	env.ReconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().
			WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
			WithSDKKey(credential.SDKKeyParams{Value: nonAnchorKey1}).
			WithSDKKey(credential.SDKKeyParams{Value: nonAnchorKey2})))

	// No new upstream client was created for the non-anchor keys.
	if !helpers.AssertNoMoreValues(t, clientCh, 200*time.Millisecond) {
		t.FailNow()
	}

	// GetClient still returns the anchor's client — not nil, not a non-anchor client.
	assert.Equal(t, anchorClient, env.GetClient())
}

func TestNonPrimaryMobileKeyDoesNotStealEventForwarding(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	envConfig := st.EnvWithAllCredentials.Config
	primaryMobile := envConfig.MobileKey
	nonPrimaryMobile := config.MobileKey("mob-non-primary")

	eventRecorderHandler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(eventRecorderHandler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.SendEvents = true
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		allConfig.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:      EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:        envConfig,
			AllConfig:        allConfig,
			ClientFactory:    testclient.FakeLDClientFactory(true),
			Loggers:          mockLog.Loggers,
			ConnectionMapper: mockConnectionMapper{},
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)

		// Reconcile to a set that keeps the original mobile key as primary but also accepts a second,
		// non-primary mobile key. Accepting the non-primary key must NOT repoint event forwarding —
		// events collapse to the primary mobile key, mirroring the SDK anchor.
		env.ReconcileCredentials(mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().
			WithAnchor(credential.SDKKeyParams{Value: envConfig.SDKKey}).
			WithPrimaryMobileKey(credential.MobileKeyParams{Value: primaryMobile}).
			WithMobileKey(credential.MobileKeyParams{Value: nonPrimaryMobile}).
			WithEnvironmentID(envConfig.EnvID)))

		ed := envImpl.GetEventDispatcher()
		require.NotNil(t, ed)
		handler := ed.GetHandler(basictypes.MobileSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, handler)

		rr := httptest.NewRecorder()
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Authorization", string(primaryMobile))
		headers.Set("X-LaunchDarkly-Event-Schema", strconv.Itoa(events.SummaryEventsSchemaVersion))
		body := `[{"kind":"identify","creationDate":1000,"key":"userkey","user":{"key":"userkey"}}]`
		req := st.BuildRequest("POST", server.URL+"/mobile/events/bulk", []byte(body), headers)
		handler(rr, req)
		require.Equal(t, 202, rr.Result().StatusCode)

		// Mobile events forward under the env's primary mobile key, not the freshly-accepted
		// non-primary one.
		eventPost := helpers.RequireValue(t, requestsCh, time.Second)
		assert.Equal(t, string(primaryMobile), eventPost.Request.Header.Get("Authorization"))
	})
}

// When an SDK key that is still alive in its grace period is re-anchored back into the primary slot,
// a fresh SDK client is started for it. The previously-created client for that same key must be closed
// rather than silently dropped from the clients map, otherwise its upstream connection leaks.
// Originally a regression test from #716 for the old UpdateCredential path, where re-anchoring to a
// key still in its grace period spawned a fresh client and orphaned the old one. Under the
// ReconcileCredentials model that leak is structurally impossible: re-anchoring to a still-accepted key
// emits no "addition", so its existing client is reused rather than re-spawned, and the displaced
// anchor's client is closed by removeCredential. This test now verifies that reuse-and-no-leak guarantee.
func TestReAnchoringToKeyStillInGraceReusesItsClient(t *testing.T) {
	envConfig := st.EnvMain.Config
	keyA := envConfig.SDKKey
	keyB := config.SDKKey("keyB")
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	clientA1 := requireClientReady(t, clientCh)
	assert.Equal(t, env.GetClient(), clientA1)

	start := time.Unix(1000, 0)

	// Rotate keyA -> keyB, deprecating keyA with an hour-long grace. keyA's client (clientA1) stays
	// alive because keyA is still accepted during the grace window.
	env.(*envContextImpl).reconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().
			WithAnchor(credential.SDKKeyParams{Value: keyB}).
			WithSDKKey(credential.SDKKeyParams{Value: keyA, Expiry: util.PtrOrNil(start.Add(1 * time.Hour))})),
		start)

	clientB := requireClientReady(t, clientCh)
	assert.NotEqual(t, clientA1, clientB)
	if !helpers.AssertChannelNotClosed(t, clientA1.CloseCh, time.Second, "clientA1 should still be alive during keyA's grace") {
		t.FailNow()
	}

	// Re-anchor back to keyA while it is still within its grace period. Because keyA is still an accepted
	// credential, its existing client (clientA1) is reused as the anchor client rather than a new one
	// being started -- so there is no stale client to orphan. keyB is omitted from the set (no expiry),
	// so it is revoked immediately and its client is closed.
	env.(*envContextImpl).reconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: keyA})),
		start.Add(10*time.Minute))

	// keyB was revoked by the re-anchor, so its client is closed.
	if !helpers.AssertChannelClosed(t, clientB.CloseCh, time.Second, "client for the revoked keyB should have been closed") {
		t.FailNow()
	}
	// clientA1 is reused, not closed or churned: re-anchoring to a still-accepted key must not tear down
	// its working upstream connection.
	if !helpers.AssertChannelNotClosed(t, clientA1.CloseCh, time.Second, "clientA1 should be reused as the anchor client, not closed") {
		t.FailNow()
	}
	// No new client is started for keyA -- the existing one is reused.
	if !helpers.AssertNoMoreValues(t, clientCh, time.Second, "re-anchoring to an in-grace key must not start a new client") {
		t.FailNow()
	}

	require.Eventually(t, func() bool {
		return env.GetClient() == clientA1
	}, time.Second, 10*time.Millisecond, "env.GetClient() should return the reused client for keyA after re-anchor")

	creds := env.GetCredentials()
	assert.Contains(t, creds, keyA)
	assert.NotContains(t, creds, keyB)
}

// gatedClientFactory wraps the normal fake factory but blocks the factory call for gateKey until
// `gate` is closed, signalling on `started` once that call is in flight. This lets a test interleave
// a credential revocation with an in-flight startSDKClient that has not yet taken c.mu.
func gatedClientFactory(
	gate <-chan struct{},
	started chan<- struct{},
	createdCh chan<- *testclient.FakeLDClient,
	gateKey config.SDKKey,
) sdks.ClientFactoryFunc {
	inner := testclient.FakeLDClientFactoryWithChannel(true, createdCh)
	var once sync.Once
	return func(sdkKey config.SDKKey, cfg ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		if sdkKey == gateKey {
			once.Do(func() { close(started) })
			<-gate
		}
		return inner(sdkKey, cfg, timeout)
	}
}

// When an SDK key is revoked while its client is still being constructed, startSDKClient builds the
// client before taking c.mu, so it can finish and try to install a client for a key that is no longer
// tracked. That client must be closed rather than installed -- otherwise it leaks its upstream
// connection and goroutines, because removeCredential already ran and found nothing in c.clients to
// close, and nothing else will ever close it until env.Close().
func TestRevokingSDKKeyWhileClientIsStartingDoesNotLeakTheClient(t *testing.T) {
	envConfig := st.EnvMain.Config
	keyA := envConfig.SDKKey
	keyB := config.SDKKey("keyB")
	readyCh := make(chan EnvContext, 1)

	clientCh := make(chan *testclient.FakeLDClient, 10)
	gate := make(chan struct{})
	started := make(chan struct{})
	factory := gatedClientFactory(gate, started, clientCh, keyA)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, factory, mockLog.Loggers, readyCh)
	defer env.Close()

	// Wait until the initial startSDKClient(keyA) is blocked inside the factory, before it takes c.mu.
	<-started

	// Revoke keyA by rotating to keyB with no grace. keyA is immediately revoked and removeCredential(keyA)
	// runs now -- but c.clients[keyA] is still nil because the initial goroutine is blocked in the factory,
	// so nothing is closed and the mapping is simply removed.
	env.(*envContextImpl).reconcileCredentials(
		mustBuildAcceptedSet(t, credential.NewAcceptedSetBuilder().WithAnchor(credential.SDKKeyParams{Value: keyB})),
		time.Unix(1000, 0))

	creds := env.GetCredentials()
	require.NotContains(t, creds, keyA, "keyA should have been revoked")

	// Release the gate; the now-unblocked startSDKClient(keyA) discovers keyA is no longer tracked and
	// must close the client it just built rather than installing it.
	close(gate)

	// Collect the client that was created for keyA.
	var clientA *testclient.FakeLDClient
	require.Eventually(t, func() bool {
		for {
			select {
			case c := <-clientCh:
				if c.Key == keyA {
					clientA = c
				}
			default:
				return clientA != nil
			}
		}
	}, 2*time.Second, 10*time.Millisecond, "expected a client to be created for keyA")
	require.NotNil(t, clientA)

	// The client built for the now-revoked keyA must be closed, not leaked.
	if !helpers.AssertChannelClosed(t, clientA.CloseCh, time.Second,
		"client built for the revoked keyA should have been closed rather than installed") {
		t.FailNow()
	}

	// And it must not have been installed into the clients map.
	impl := env.(*envContextImpl)
	impl.mu.Lock()
	_, present := impl.clients[keyA]
	impl.mu.Unlock()
	assert.False(t, present, "revoked keyA should not have a client in the clients map")
}

// An environment configured without an SDK key (e.g. offline or not-yet-configured envs, and test
// fixtures) must still get its SDK client installed. An undefined key is never a tracked credential,
// so the startup guard that discards clients for revoked keys must not fire for it.
func TestEnvWithoutSDKKeyStillInstallsClient(t *testing.T) {
	readyCh := make(chan EnvContext, 1)
	clientCh := make(chan *testclient.FakeLDClient, 1)
	clientFactory := testclient.FakeLDClientFactoryWithChannel(true, clientCh)

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, config.EnvConfig{}, clientFactory, mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	client := requireClientReady(t, clientCh)
	assert.NotNil(t, client)
	assert.Equal(t, client, env.GetClient())
}

func TestSDKClientCreationFails(t *testing.T) {
	envConfig := st.EnvWithAllCredentials.Config
	envConfig.TTL = configtypes.NewOptDuration(time.Hour)
	envConfig.SecureMode = true
	readyCh := make(chan EnvContext, 1)

	fakeError := errors.New("sorry")

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env := makeBasicEnv(t, envConfig, testclient.ClientFactoryThatFails(fakeError), mockLog.Loggers, readyCh)
	defer env.Close()

	assert.Equal(t, env, requireEnvReady(t, readyCh))
	assert.Equal(t, fakeError, env.GetInitError())
	assert.Nil(t, env.GetStore())
}

func TestDisplayName(t *testing.T) {
	ei1 := EnvIdentifiers{ProjName: "a", EnvName: "b", ConfiguredName: "thing"}
	assert.Equal(t, "thing", ei1.GetDisplayName())

	ei2 := EnvIdentifiers{ProjName: "a", EnvName: "b"}
	assert.Equal(t, "a b", ei2.GetDisplayName())
}

func TestMetricsAreExportedForEnvironment(t *testing.T) {
	// We already have tests for openCensusEventsExporter in the metrics package, but this test verifies that
	// exporting is configured automatically for every environment that we add (if not disabled).
	view.SetReportingPeriod(time.Millisecond * 10)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	fakeUserAgent := "fake-user-agent"

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		metricsManager, err := metrics.NewManager(config.MetricsConfig{}, time.Minute, mockLog.Loggers)
		require.NoError(t, err)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:    EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:      st.EnvMain.Config,
			AllConfig:      allConfig,
			ClientFactory:  testclient.FakeLDClientFactory(true),
			MetricsManager: metricsManager,
			UserAgent:      fakeUserAgent,
			Loggers:        mockLog.Loggers,
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)
		metrics.WithCount(env.GetMetricsContext(), fakeUserAgent, "", func() {
			require.Eventually(t, func() bool {
				flushMetricsEvents(envImpl)
				select {
				case req := <-requestsCh:
					mockLog.Loggers.Infof("received metrics events: %s", req.Body)
					uncompressed, err := util.DecompressGzipData(req.Body)
					require.NoError(t, err)

					data := ldvalue.Parse(uncompressed)
					event := data.GetByIndex(0)
					if !event.IsNull() {
						conns := event.GetByKey("connections")
						return event.GetByKey("kind").StringValue() == "relayMetrics" &&
							conns.Count() == 1 &&
							conns.GetByIndex(0).GetByKey("userAgent").StringValue() == fakeUserAgent &&
							conns.GetByIndex(0).GetByKey("current").IntValue() == 1
					}
				default:
					break
				}
				return false
			}, time.Second, time.Millisecond*10, "timed out waiting for metrics event with counter")
		}, metrics.BrowserConns)
	})
}

func TestMetricsAreNotExportedForEnvironmentInOfflineMode(t *testing.T) {
	var allConfig config.Config
	allConfig.OfflineMode.FileDataSource = "fake-file-path"
	testMetricsDisabled(t, allConfig)
}

func testMetricsDisabled(t *testing.T, allConfig config.Config) {
	view.SetReportingPeriod(time.Millisecond * 10)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	fakeUserAgent := "fake-user-agent"

	handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(handler, func(server *httptest.Server) {
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		metricsManager, err := metrics.NewManager(config.MetricsConfig{}, time.Minute, mockLog.Loggers)
		require.NoError(t, err)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:    EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:      st.EnvMain.Config,
			AllConfig:      allConfig,
			ClientFactory:  testclient.FakeLDClientFactory(true),
			MetricsManager: metricsManager,
			Loggers:        mockLog.Loggers,
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)
		metrics.WithCount(env.GetMetricsContext(), fakeUserAgent, "", func() {
			require.Never(t, func() bool {
				flushMetricsEvents(envImpl)
				select {
				case <-requestsCh:
					return true
				default:
					break
				}
				return false
			}, time.Millisecond*100, time.Millisecond*10, "received unexpected metrics event")
		}, metrics.BrowserConns)
	})
}

func TestEventDispatcherIsCreatedIfSendEventsIsTrueAndNotInOfflineMode(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	eventRecorderHandler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(eventRecorderHandler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.Events.SendEvents = true
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		allConfig.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:   EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:     st.EnvMain.Config,
			AllConfig:     allConfig,
			ClientFactory: testclient.FakeLDClientFactory(true),
			Loggers:       mockLog.Loggers,
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)

		ed := envImpl.GetEventDispatcher()
		require.NotNil(t, ed)
		eventDispatchHandler := ed.GetHandler(basictypes.ServerSDK, ldevents.AnalyticsEventDataKind)
		require.NotNil(t, eventDispatchHandler)

		rr := httptest.NewRecorder()
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Authorization", string(st.EnvMain.Config.SDKKey))
		headers.Set("X-LaunchDarkly-Event-Schema", strconv.Itoa(events.SummaryEventsSchemaVersion))
		body := `[{"kind":"identify","creationDate":1000,"key":"userkey","user":{"key":"userkey"}}]`
		req := st.BuildRequest("POST", server.URL+"/bulk", []byte(body), headers)
		eventDispatchHandler(rr, req)
		require.Equal(t, 202, rr.Result().StatusCode)

		// Because the event schema version is >= 3, the event data should be forwarded verbatim with no processing.
		eventPost := helpers.RequireValue(t, requestsCh, time.Second)
		require.Equal(t, string(st.EnvMain.Config.SDKKey), eventPost.Request.Header.Get("Authorization"))

		decodedBody, err := util.DecompressGzipData(eventPost.Body)
		require.NoError(t, err)
		require.Equal(t, string(body), string(decodedBody))
	})
}

func TestEventDispatcherIsNotCreatedIfSendEventsIsTrueAndNotInOfflineMode(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	eventRecorderHandler, _ := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(202))
	httphelpers.WithServer(eventRecorderHandler, func(server *httptest.Server) {
		var allConfig config.Config
		allConfig.OfflineMode.FileDataSource = "fake-file-path"
		allConfig.Events.SendEvents = true
		allConfig.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(server.URL)
		allConfig.Events.FlushInterval = configtypes.NewOptDuration(time.Millisecond * 10)
		env, err := NewEnvContext(EnvContextImplParams{
			Identifiers:   EnvIdentifiers{ConfiguredName: envName},
			EnvConfig:     st.EnvMain.Config,
			AllConfig:     allConfig,
			ClientFactory: testclient.FakeLDClientFactory(true),
			Loggers:       mockLog.Loggers,
		}, nil)
		require.NoError(t, err)
		defer env.Close()
		envImpl := env.(*envContextImpl)

		ed := envImpl.GetEventDispatcher()
		require.Nil(t, ed)
	})
}

func TestBigSegmentsSynchronizerIsCreatedIfBigSegmentStoreExists(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		Loggers: mockLog.Loggers,
	}, nil)
	require.NoError(t, err)

	if assert.NotNil(t, fakeSynchronizerFactory.synchronizer) {
		assert.False(t, fakeSynchronizerFactory.synchronizer.isStarted())
		assert.False(t, fakeSynchronizerFactory.synchronizer.isClosed())

		// We shouldn't expose the store until some big segments exist, so that Relay doesn't report
		// misleading big segments status info in its status resource.
		assert.Nil(t, env.GetBigSegmentStore())
	}

	env.Close()

	assert.True(t, fakeSynchronizerFactory.synchronizer.isClosed())
}

func TestBigSegmentsSynchronizerIsStartedByFullDataUpdateWithBigSegment(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		Loggers: mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)
	assert.False(t, synchronizer.isStarted())

	// Simulate receiving some data
	updates := env.(*envContextImpl).storeAdapter.GetUpdates()

	s1 := ldbuilders.NewSegmentBuilder("s1").Build()
	dataWithNoBigSegment := []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: "s1", Item: st.SegmentDesc(s1)},
			},
		},
	}
	updates.SendAllDataUpdate(dataWithNoBigSegment)

	assert.False(t, synchronizer.isStarted())

	s2 := ldbuilders.NewSegmentBuilder("s2").Unbounded(true).Generation(1).Build()
	dataWithBigSegment := []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: "s1", Item: st.SegmentDesc(s1)},
				{Key: "s2", Item: st.SegmentDesc(s2)},
			},
		},
	}
	updates.SendAllDataUpdate(dataWithBigSegment)

	assert.True(t, synchronizer.isStarted())

	// Now we should expose the big segment store so that Relay can include big segment status information
	// in its status resource.
	assert.NotNil(t, env.GetBigSegmentStore())
}

func TestBigSegmentsSynchronizerIsStartedBySingleItemUpdateWithBigSegment(t *testing.T) {
	envConfig := st.EnvMain.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		Loggers: mockLog.Loggers,
	}, nil)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)
	assert.False(t, synchronizer.isStarted())

	// Simulate receiving some data
	updates := env.(*envContextImpl).storeAdapter.GetUpdates()

	f1 := ldbuilders.NewFlagBuilder("f1").Build()
	updates.SendSingleItemUpdate(ldstoreimpl.Features(), f1.Key, st.FlagDesc(f1))

	assert.False(t, synchronizer.isStarted())

	s1 := ldbuilders.NewSegmentBuilder("s1").Build()
	updates.SendSingleItemUpdate(ldstoreimpl.Segments(), s1.Key, st.SegmentDesc(s1))

	assert.False(t, synchronizer.isStarted())

	s2 := ldbuilders.NewSegmentBuilder("s2").Unbounded(true).Generation(1).Build()
	updates.SendSingleItemUpdate(ldstoreimpl.Segments(), s2.Key, st.SegmentDesc(s2))

	assert.True(t, synchronizer.isStarted())
}

func TestReceivingBigSegmentsUpdateCausesClientSideInvalidationEvent(t *testing.T) {
	envConfig := st.EnvClientSide.Config
	allConfig := config.Config{}

	fakeBigSegmentStoreFactory := func(config.EnvConfig, config.Config, ldlog.Loggers) (bigsegments.BigSegmentStore, error) {
		return bigsegments.NewNullBigSegmentStore(), nil
	}
	fakeSynchronizerFactory := &mockBigSegmentSynchronizerFactory{}

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)

	jsClientStreams := streams.NewStreamProvider(basictypes.JSClientPingStream, time.Hour, 0)
	sdkStartedCh := make(chan EnvContext)
	env, err := NewEnvContext(EnvContextImplParams{
		Identifiers:                   EnvIdentifiers{ConfiguredName: st.EnvMain.Name},
		EnvConfig:                     envConfig,
		AllConfig:                     allConfig,
		BigSegmentStoreFactory:        fakeBigSegmentStoreFactory,
		BigSegmentSynchronizerFactory: fakeSynchronizerFactory.create,
		ClientFactory:                 testclient.FakeLDClientFactory(true),
		SDKBigSegmentsConfigFactory: ldcomponents.BigSegments(
			st.ExistingInstance[subsystems.BigSegmentStore](&st.NoOpSDKBigSegmentStore{}),
		),
		StreamProviders: []streams.StreamProvider{jsClientStreams},
		Loggers:         mockLog.Loggers,
	}, sdkStartedCh)
	require.NoError(t, err)
	defer env.Close()

	synchronizer := fakeSynchronizerFactory.synchronizer
	require.NotNil(t, synchronizer)

	streamHandler := env.GetStreamHandler(jsClientStreams, envConfig.EnvID)

	// Make sure the data store is initialized, otherwise the client-side endpoint won't broadcast a ping
	<-sdkStartedCh
	_ = env.GetStore().Init(nil)

	req, _ := http.NewRequest("GET", "", nil)
	st.WithStreamRequest(t, req, streamHandler, func(eventCh <-chan eventsource.Event) {
		initEvent := helpers.RequireValue(t, eventCh, time.Minute)
		assert.Equal(t, "ping", initEvent.Event())

		if !helpers.AssertNoMoreValues(t, eventCh, time.Millisecond*100) {
			t.FailNow()
		}

		synchronizer.updateCh <- bigsegments.UpdatesSummary{SegmentKeysUpdated: []string{"fake-segment-key"}}

		pingEvent := helpers.RequireValue(t, eventCh, time.Second)
		assert.Equal(t, "ping", pingEvent.Event())
	})
}

// This method forces the metrics events exporter to post an event to the event publisher, and then triggers a
// flush of the event publisher. Because both of those actions are asynchronous, it may be necessary to call it
// more than once to ensure that the newly posted event is included in the flush.
func flushMetricsEvents(c *envContextImpl) {
	if c.metricsEventPub != nil {
		c.metricsEnv.FlushEventsExporter()
		c.metricsEventPub.Flush()
	}
}

type mockBigSegmentSynchronizerFactory struct {
	synchronizer *mockBigSegmentSynchronizer
}

func (f *mockBigSegmentSynchronizerFactory) create(
	httpConfig httpconfig.HTTPConfig,
	store bigsegments.BigSegmentStore,
	pollURI string,
	streamURI string,
	envID config.EnvironmentID,
	sdkKey config.SDKKey,
	loggers ldlog.Loggers,
	logPrefix string,
) bigsegments.BigSegmentSynchronizer {
	f.synchronizer = &mockBigSegmentSynchronizer{updateCh: make(chan bigsegments.UpdatesSummary)}
	return f.synchronizer
}

type mockBigSegmentSynchronizer struct {
	started  bool
	closed   bool
	updateCh chan bigsegments.UpdatesSummary
	lock     sync.Mutex
}

func (s *mockBigSegmentSynchronizer) Start() {
	s.lock.Lock()
	s.started = true
	s.lock.Unlock()
}

func (s *mockBigSegmentSynchronizer) HasSynced() bool {
	return true
}

func (s *mockBigSegmentSynchronizer) SegmentUpdatesCh() <-chan bigsegments.UpdatesSummary {
	return s.updateCh
}

func (s *mockBigSegmentSynchronizer) Close() {
	s.lock.Lock()
	s.closed = true
	s.lock.Unlock()
}

func (s *mockBigSegmentSynchronizer) isStarted() bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.started
}

func (s *mockBigSegmentSynchronizer) isClosed() bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.closed
}
