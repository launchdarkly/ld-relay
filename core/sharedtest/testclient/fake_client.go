// Package testclient contains test helpers that reference the SDK-related packages. These are in
// sharedtest/testclient rather than just sharedtest so that sharedtest can be used by those packages
// without a circular reference.
package testclient

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/internal/store"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/testhelpers"
)

func CreateDummyClient(sdkKey config.SDKKey, sdkConfig ld.Config) (sdks.LDClientContext, error) {
	store, _ := sdkConfig.DataStore.(*store.SSERelayDataStoreAdapter).CreateDataStore(
		testhelpers.NewSimpleClientContext(string(sdkKey)), nil)
	err := store.Init(sharedtest.AllData)
	if err != nil {
		panic(err)
	}
	return &FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{}), initialized: true}, nil
}

type FakeLDClient struct {
	Key              config.SDKKey
	CloseCh          chan struct{}
	dataSourceStatus *interfaces.DataSourceStatus
	initialized      bool
	lock             sync.Mutex
}

func (c *FakeLDClient) Initialized() bool {
	return c.initialized
}

func (c *FakeLDClient) SecureModeHash(user lduser.User) string {
	return FakeHashForUser(user)
}

func (c *FakeLDClient) GetDataSourceStatus() interfaces.DataSourceStatus {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.dataSourceStatus != nil {
		return *c.dataSourceStatus
	}
	state := interfaces.DataSourceStateValid
	if !c.initialized {
		state = interfaces.DataSourceStateInitializing
	}
	return interfaces.DataSourceStatus{State: state}
}

func (c *FakeLDClient) GetDataStoreStatus() sdks.DataStoreStatusInfo {
	return sdks.DataStoreStatusInfo{Available: true}
}

func (c *FakeLDClient) Close() error {
	if c.CloseCh != nil {
		close(c.CloseCh)
	}
	return nil
}

func (c *FakeLDClient) SetDataSourceStatus(newStatus interfaces.DataSourceStatus) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.dataSourceStatus = &newStatus
}

func (c *FakeLDClient) AwaitClose(t *testing.T, timeout time.Duration) {
	select {
	case <-c.CloseCh:
		return
	case <-time.After(timeout):
		require.Fail(t, "timed out waiting for SDK client to be closed")
	}
}

func FakeHashForUser(user lduser.User) string {
	return "fake-hash-" + user.GetKey()
}

func FakeLDClientFactory(shouldBeInitialized bool) sdks.ClientFactoryFunc {
	return FakeLDClientFactoryWithChannel(shouldBeInitialized, nil)
}

func FakeLDClientFactoryWithChannel(shouldBeInitialized bool, createdCh chan<- *FakeLDClient) sdks.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config) (sdks.LDClientContext, error) {
		// We're not creating a real client, but we still need to invoke the DataStoreFactory as the
		// SDK would do, since that's how Relay obtains its shared reference to the data store.
		if config.DataStore != nil {
			_, err := config.DataStore.CreateDataStore(
				sharedtest.SDKContextImpl{},
				nil,
			)
			if err != nil {
				return nil, err
			}
		}
		c := &FakeLDClient{Key: sdkKey, CloseCh: make(chan struct{}), initialized: shouldBeInitialized}
		if createdCh != nil {
			createdCh <- c
		}
		return c, nil
	}
}

func ClientFactoryThatFails(err error) sdks.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config) (sdks.LDClientContext, error) {
		return nil, err
	}
}
