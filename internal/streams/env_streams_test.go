package streams

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStreamProvider struct {
	credentialOfDesiredType credential.SDKCredential
	createdStreamsV1        []*mockEnvStreamProvider
	createdStreamsV2        []*mockEnvStreamProvider
}

type mockEnvStreamProvider struct {
	parent         *mockStreamProvider
	credential     sdkauth.ScopedCredential
	store          EnvStoreQueries
	allDataUpdates [][]subsystems.Change
	itemUpdates    []subsystems.Change
	clientSideUps  int
	numHeartbeats  int
	closed         bool
	lock           sync.Mutex
}

func (p *mockStreamProvider) HandlerV1(credential sdkauth.ScopedCredential) http.HandlerFunc {
	return nil
}

func (p *mockStreamProvider) HandlerV2(credential sdkauth.ScopedCredential) http.HandlerFunc {
	return nil
}

func (p *mockStreamProvider) RegisterV1(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if reflect.TypeOf(credential.SDKCredential) != reflect.TypeOf(p.credentialOfDesiredType) {
		return nil
	}
	esp := &mockEnvStreamProvider{parent: p, credential: credential, store: store}
	p.createdStreamsV1 = append(p.createdStreamsV1, esp)
	return esp
}

func (p *mockStreamProvider) RegisterV2(
	credential sdkauth.ScopedCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if reflect.TypeOf(credential.SDKCredential) != reflect.TypeOf(p.credentialOfDesiredType) {
		return nil
	}
	esp := &mockEnvStreamProvider{parent: p, credential: credential, store: store}
	p.createdStreamsV2 = append(p.createdStreamsV2, esp)
	return esp
}

func (p *mockStreamProvider) Close() {}

func (e *mockEnvStreamProvider) Apply(changeSet subsystems.ChangeSet) {
	switch changeSet.IntentCode() {
	case subsystems.IntentTransferFull:
		e.allDataUpdates = append(e.allDataUpdates, changeSet.Changes())
	case subsystems.IntentTransferChanges:
		e.itemUpdates = append(e.itemUpdates, changeSet.Changes()...)
	}
}

func (e *mockEnvStreamProvider) InvalidateClientSideState() {
	e.clientSideUps++
}

func (e *mockEnvStreamProvider) SendHeartbeat() {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.numHeartbeats++
}

func (e *mockEnvStreamProvider) Close() {
	e.closed = true
}

func (e *mockEnvStreamProvider) getNumHeartbeats() int {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.numHeartbeats
}

func TestAddCredential(t *testing.T) {
	sp1 := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}
	sp2 := &mockStreamProvider{credentialOfDesiredType: config.MobileKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp1, sp2}, store, 0, config.DefaultFilter, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key1")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)

	mobileKey := config.MobileKey("mobile-key")
	es.AddCredential(mobileKey)

	unsupportedKey := config.EnvironmentID("x")
	es.AddCredential(unsupportedKey)

	es.AddCredential(nil)

	require.Len(t, sp1.createdStreamsV1, 2)
	esp1, esp2 := sp1.createdStreamsV1[0], sp1.createdStreamsV1[1]
	assert.Equal(t, sdkKey1, esp1.credential.SDKCredential)
	assert.Equal(t, sdkKey2, esp2.credential.SDKCredential)
	assert.Equal(t, store, esp1.store)
	assert.Equal(t, store, esp2.store)

	require.Len(t, sp1.createdStreamsV2, 2)
	esp1V2, esp2V2 := sp1.createdStreamsV2[0], sp1.createdStreamsV2[1]
	assert.Equal(t, sdkKey1, esp1V2.credential.SDKCredential)
	assert.Equal(t, sdkKey2, esp2V2.credential.SDKCredential)
	assert.Equal(t, store, esp1V2.store)
	assert.Equal(t, store, esp2V2.store)

	require.Len(t, sp2.createdStreamsV1, 1)
	esp3 := sp2.createdStreamsV1[0]
	assert.Equal(t, mobileKey, esp3.credential.SDKCredential)
	assert.Equal(t, store, esp3.store)

	require.Len(t, sp2.createdStreamsV2, 1)
	esp3V2 := sp2.createdStreamsV2[0]
	assert.Equal(t, mobileKey, esp3V2.credential.SDKCredential)
	assert.Equal(t, store, esp3V2.store)
}

func TestRemoveCredential(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, config.DefaultFilter, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)

	require.Len(t, sp.createdStreamsV1, 2)
	esp1, esp2 := sp.createdStreamsV1[0], sp.createdStreamsV1[1]
	assert.Equal(t, sdkKey1, esp1.credential.SDKCredential)
	assert.Equal(t, sdkKey2, esp2.credential.SDKCredential)
	assert.False(t, esp1.closed)
	assert.False(t, esp2.closed)

	require.Len(t, sp.createdStreamsV2, 2)
	esp1V2, esp2V2 := sp.createdStreamsV2[0], sp.createdStreamsV2[1]
	assert.Equal(t, sdkKey1, esp1V2.credential.SDKCredential)
	assert.Equal(t, sdkKey2, esp2V2.credential.SDKCredential)
	assert.False(t, esp1V2.closed)
	assert.False(t, esp2V2.closed)

	es.RemoveCredential(sdkKey2)
	assert.False(t, esp1.closed)
	assert.True(t, esp2.closed)
}

func TestCloseEnvStreamsClosesAll(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, config.DefaultFilter, ldlog.NewDisabledLoggers())

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreamsV1, 3)
	esp1, esp2, esp3 := sp.createdStreamsV1[0], sp.createdStreamsV1[1], sp.createdStreamsV1[2]
	require.Len(t, sp.createdStreamsV2, 3)
	esp1V2, esp2V2, esp3V2 := sp.createdStreamsV2[0], sp.createdStreamsV2[1], sp.createdStreamsV2[2]

	es.RemoveCredential(sdkKey2)
	esp2.closed = false
	esp2V2.closed = false
	assert.False(t, esp1.closed)
	assert.False(t, esp3.closed)
	assert.False(t, esp1V2.closed)
	assert.False(t, esp3V2.closed)

	es.Close()

	assert.True(t, esp1.closed)
	assert.True(t, esp1V2.closed)
	assert.True(t, esp3.closed)
	assert.True(t, esp3V2.closed)
	assert.False(t, esp2.closed)
	assert.False(t, esp2V2.closed)
}

func TestSetBasisGoesToAllStreams(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, config.DefaultFilter, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreamsV1, 3)
	esp1, esp2, esp3 := sp.createdStreamsV1[0], sp.createdStreamsV1[1], sp.createdStreamsV1[2]
	require.Len(t, sp.createdStreamsV2, 3)
	esp1V2, esp2V2, esp3V2 := sp.createdStreamsV2[0], sp.createdStreamsV2[1], sp.createdStreamsV2[2]

	es.RemoveCredential(sdkKey2)

	es.Apply(*fdv2ChangeSet)
	expected := [][]subsystems.Change{fdv2AllData}

	assert.Equal(t, expected, esp1.allDataUpdates)
	assert.Equal(t, expected, esp1V2.allDataUpdates)
	assert.Len(t, esp2.allDataUpdates, 0)
	assert.Len(t, esp2V2.allDataUpdates, 0)
	assert.Equal(t, expected, esp3.allDataUpdates)
	assert.Equal(t, expected, esp3V2.allDataUpdates)
}

func TestApplyDeltaGoesToAllStreams(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, config.DefaultFilter, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreamsV1, 3)
	esp1, esp2, esp3 := sp.createdStreamsV1[0], sp.createdStreamsV1[1], sp.createdStreamsV1[2]
	require.Len(t, sp.createdStreamsV2, 3)
	esp1V2, esp2V2, esp3V2 := sp.createdStreamsV2[0], sp.createdStreamsV2[1], sp.createdStreamsV2[2]

	es.RemoveCredential(sdkKey2)

	changeSet, err := subsystems.NewChangeSetBuilder().Start(subsystems.ServerIntent{
		Payload: subsystems.Payload{
			ID:     "state",
			Target: 1,
			Code:   subsystems.IntentTransferChanges,
			Reason: "stale",
		},
	}).
		AddPut(subsystems.FlagKind, testFlag1.Key, 0, testFlag1JSON).
		Finish(subsystems.NewSelector("state", 1))
	assert.NoError(t, err)

	es.Apply(*changeSet)
	expected := []subsystems.Change{
		{Action: subsystems.ChangeTypePut, Kind: subsystems.FlagKind, Key: testFlag1.Key, Object: testFlag1JSON},
	}

	assert.Equal(t, expected, esp1.itemUpdates)
	assert.Equal(t, expected, esp1V2.itemUpdates)
	assert.Len(t, esp2.itemUpdates, 0)
	assert.Len(t, esp2V2.itemUpdates, 0)
	assert.Equal(t, expected, esp3.itemUpdates)
	assert.Equal(t, expected, esp3V2.itemUpdates)
}

func TestInvalidateClientSideStateGoesToAllStreams(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, config.DefaultFilter, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreamsV1, 3)
	esp1, esp2, esp3 := sp.createdStreamsV1[0], sp.createdStreamsV1[1], sp.createdStreamsV1[2]
	require.Len(t, sp.createdStreamsV2, 3)
	esp1V2, esp2V2, esp3V2 := sp.createdStreamsV2[0], sp.createdStreamsV2[1], sp.createdStreamsV2[2]

	es.RemoveCredential(sdkKey2)

	es.InvalidateClientSideState()

	assert.Equal(t, 1, esp1.clientSideUps)
	assert.Equal(t, 0, esp2.clientSideUps)
	assert.Equal(t, 1, esp3.clientSideUps)
	assert.Equal(t, 1, esp1V2.clientSideUps)
	assert.Equal(t, 0, esp2V2.clientSideUps)
	assert.Equal(t, 1, esp3V2.clientSideUps)
}

func TestHeartbeatsGoToAllStreams(t *testing.T) {
	heartbeatInterval := time.Millisecond * 20

	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, heartbeatInterval, config.DefaultFilter, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)

	require.Len(t, sp.createdStreamsV1, 2)
	esp1, esp2 := sp.createdStreamsV1[0], sp.createdStreamsV1[1]
	require.Len(t, sp.createdStreamsV2, 2)
	esp1V2, esp2V2 := sp.createdStreamsV2[0], sp.createdStreamsV2[1]

	var count1, count2, count1V2, count2V2 int
	if !assert.Eventually(t,
		func() bool {
			count1 = esp1.getNumHeartbeats()
			count2 = esp2.getNumHeartbeats()
			count1V2 = esp1V2.getNumHeartbeats()
			count2V2 = esp2V2.getNumHeartbeats()
			return count1 >= 2 && count2 >= 2 && count1V2 >= 2 && count2V2 >= 2
		},
		time.Second,
		time.Millisecond*20,
		"Waited to see at least 2 heartbeats received by each stream") {
		assert.Fail(t, "Got only %d, %d, %d, and %d heartbeats", count1, count2, count1V2, count2V2)
	}
}

func TestHeartbeatsAreStopped(t *testing.T) {
	heartbeatInterval := time.Millisecond * 20

	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, heartbeatInterval, config.DefaultFilter, ldlog.NewDisabledLoggers())

	es.AddCredential(config.SDKKey("sdk-key1"))

	require.Len(t, sp.createdStreamsV1, 1)
	esp1 := sp.createdStreamsV1[0]
	require.Len(t, sp.createdStreamsV2, 1)
	esp1V2 := sp.createdStreamsV2[0]

	// Give the heartbeat goroutine time to start and send at least one heartbeat
	assert.Eventually(t, func() bool { return esp1.getNumHeartbeats() >= 1 && esp1V2.getNumHeartbeats() >= 1 }, time.Second, time.Millisecond*20,
		"Waited for heartbeats to start but timed out without seeing any")

	es.Close()

	helpers.AssertChannelClosed(t, es.heartbeatsDone, time.Second, "heartbeatsDone channel should have been closed")
}
