package streams

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStreamProvider struct {
	credentialOfDesiredType config.SDKCredential
	createdStreams          []*mockEnvStreamProvider
}

type mockEnvStreamProvider struct {
	parent         *mockStreamProvider
	credential     config.SDKCredential
	store          EnvStoreQueries
	allDataUpdates [][]ldstoretypes.Collection
	itemUpdates    []sharedtest.ReceivedItemUpdate
	clientSideUps  int
	numHeartbeats  int
	closed         bool
	lock           sync.Mutex
}

func (p *mockStreamProvider) Handler(credential config.SDKCredential) http.HandlerFunc {
	return nil
}

func (p *mockStreamProvider) Register(
	credential config.SDKCredential,
	store EnvStoreQueries,
	loggers ldlog.Loggers,
) EnvStreamProvider {
	if reflect.TypeOf(credential) != reflect.TypeOf(p.credentialOfDesiredType) {
		return nil
	}
	esp := &mockEnvStreamProvider{parent: p, credential: credential, store: store}
	p.createdStreams = append(p.createdStreams, esp)
	return esp
}

func (p *mockStreamProvider) Close() {}

func (e *mockEnvStreamProvider) SendAllDataUpdate(allData []ldstoretypes.Collection) {
	e.allDataUpdates = append(e.allDataUpdates, allData)
}

func (e *mockEnvStreamProvider) SendSingleItemUpdate(kind ldstoretypes.DataKind, key string, item ldstoretypes.ItemDescriptor) {
	e.itemUpdates = append(e.itemUpdates, sharedtest.ReceivedItemUpdate{kind, key, item})
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

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp1, sp2}, store, 0, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key1")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)

	mobileKey := config.MobileKey("mobile-key")
	es.AddCredential(mobileKey)

	unsupportedKey := config.EnvironmentID("x")
	es.AddCredential(unsupportedKey)

	es.AddCredential(nil)

	require.Len(t, sp1.createdStreams, 2)
	esp1, esp2 := sp1.createdStreams[0], sp1.createdStreams[1]
	assert.Equal(t, sdkKey1, esp1.credential)
	assert.Equal(t, sdkKey2, esp2.credential)
	assert.Equal(t, store, esp1.store)
	assert.Equal(t, store, esp2.store)

	require.Len(t, sp2.createdStreams, 1)
	esp3 := sp2.createdStreams[0]
	assert.Equal(t, mobileKey, esp3.credential)
	assert.Equal(t, store, esp3.store)
}

func TestRemoveCredential(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)

	require.Len(t, sp.createdStreams, 2)
	esp1, esp2 := sp.createdStreams[0], sp.createdStreams[1]
	assert.Equal(t, sdkKey1, esp1.credential)
	assert.Equal(t, sdkKey2, esp2.credential)
	assert.False(t, esp1.closed)
	assert.False(t, esp2.closed)

	es.RemoveCredential(sdkKey2)
	assert.False(t, esp1.closed)
	assert.True(t, esp2.closed)
}

func TestCloseEnvStreamsClosesAll(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, ldlog.NewDisabledLoggers())

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreams, 3)
	esp1, esp2, esp3 := sp.createdStreams[0], sp.createdStreams[1], sp.createdStreams[2]

	es.RemoveCredential(sdkKey2)
	esp2.closed = false
	assert.False(t, esp1.closed)
	assert.False(t, esp3.closed)

	es.Close()

	assert.True(t, esp1.closed)
	assert.True(t, esp3.closed)
	assert.False(t, esp2.closed)
}

func TestSendAllDataUpdateGoesToAllStreams(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreams, 3)
	esp1, esp2, esp3 := sp.createdStreams[0], sp.createdStreams[1], sp.createdStreams[2]

	es.RemoveCredential(sdkKey2)

	es.SendAllDataUpdate(allData)
	expected := [][]ldstoretypes.Collection{allData}

	assert.Equal(t, expected, esp1.allDataUpdates)
	assert.Len(t, esp2.allDataUpdates, 0)
	assert.Equal(t, expected, esp3.allDataUpdates)
}

func TestSendSingleItemUpdateGoesToAllStreams(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreams, 3)
	esp1, esp2, esp3 := sp.createdStreams[0], sp.createdStreams[1], sp.createdStreams[2]

	es.RemoveCredential(sdkKey2)

	es.SendSingleItemUpdate(ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1))
	expected := []sharedtest.ReceivedItemUpdate{{ldstoreimpl.Features(), testFlag1.Key, sharedtest.FlagDesc(testFlag1)}}

	assert.Equal(t, expected, esp1.itemUpdates)
	assert.Len(t, esp2.itemUpdates, 0)
	assert.Equal(t, expected, esp3.itemUpdates)
}

func TestInvalidateClientSideStateGoesToAllStreams(t *testing.T) {
	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, 0, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2, sdkKey3 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2"), config.SDKKey("sdk-key3")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)
	es.AddCredential(sdkKey3)

	require.Len(t, sp.createdStreams, 3)
	esp1, esp2, esp3 := sp.createdStreams[0], sp.createdStreams[1], sp.createdStreams[2]

	es.RemoveCredential(sdkKey2)

	es.InvalidateClientSideState()

	assert.Equal(t, 1, esp1.clientSideUps)
	assert.Equal(t, 0, esp2.clientSideUps)
	assert.Equal(t, 1, esp3.clientSideUps)
}

func TestHeartbeatsGoToAllStreams(t *testing.T) {
	heartbeatInterval := time.Millisecond * 20

	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, heartbeatInterval, ldlog.NewDisabledLoggers())
	defer es.Close()

	sdkKey1, sdkKey2 := config.SDKKey("sdk-key1"), config.SDKKey("sdk-key2")
	es.AddCredential(sdkKey1)
	es.AddCredential(sdkKey2)

	require.Len(t, sp.createdStreams, 2)
	esp1, esp2 := sp.createdStreams[0], sp.createdStreams[1]

	var count1, count2 int
	if !assert.Eventually(t,
		func() bool {
			count1 = esp1.getNumHeartbeats()
			count2 = esp2.getNumHeartbeats()
			return count1 >= 2 && count2 >= 2
		},
		time.Second,
		time.Millisecond*20,
		"Waited to see at least 2 heartbeats received by each stream") {
		assert.Fail(t, "Got only %d and %d heartbeats", count1, count2)
	}
}

func TestHeartbeatsAreStopped(t *testing.T) {
	heartbeatInterval := time.Millisecond * 20

	sp := &mockStreamProvider{credentialOfDesiredType: config.SDKKey("")}

	store := makeMockStore(nil, nil, nil, nil)
	es := NewEnvStreams([]StreamProvider{sp}, store, heartbeatInterval, ldlog.NewDisabledLoggers())

	es.AddCredential(config.SDKKey("sdk-key1"))

	require.Len(t, sp.createdStreams, 1)
	esp1 := sp.createdStreams[0]

	// Give the heartbeat goroutine time to start and send at least one heartbeat
	assert.Eventually(t, func() bool { return esp1.getNumHeartbeats() >= 1 }, time.Second, time.Millisecond*20,
		"Waited for heartbeats to start but timed out without seeing any")

	es.Close()

	helpers.AssertChannelClosed(t, es.heartbeatsDone, time.Second, "heartbeatsDone channel should have been closed")
}
