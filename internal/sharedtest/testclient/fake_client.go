// Package testclient contains test helpers that reference the SDK-related packages. These are in
// sharedtest/testclient rather than just sharedtest so that sharedtest can be used by those packages
// without a circular reference.
package testclient

import (
	"sync"
	"testing"
	"time"

	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/ld-relay/v7/config"
	"github.com/launchdarkly/ld-relay/v7/internal/sdks"
	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v7/internal/store"

	"github.com/launchdarkly/go-sdk-common/v3/ldcontext"
	ld "github.com/launchdarkly/go-server-sdk/v7"
	"github.com/launchdarkly/go-server-sdk/v7/interfaces"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
)

func CreateDummyClient(sdkKey config.SDKKey, sdkConfig ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
	store, _ := sdkConfig.DataStore.(*store.SSERelayDataStoreAdapter).Build(
		subsystems.BasicClientContext{SDKKey: string(sdkKey)})
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

type CapturedLDClient struct {
	Key    config.SDKKey
	Client sdks.LDClientContext
}

func (c *FakeLDClient) Initialized() bool {
	return c.initialized
}

func (c *FakeLDClient) SecureModeHash(context ldcontext.Context) string {
	return FakeHashForContext(context)
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
	if !helpers.AssertChannelClosed(t, c.CloseCh, timeout, "timed out waiting for SDK client to be closed") {
		t.FailNow()
	}
}

func FakeHashForContext(context ldcontext.Context) string {
	return "fake-hash-" + context.Key()
}

func FakeLDClientFactory(shouldBeInitialized bool) sdks.ClientFactoryFunc {
	return FakeLDClientFactoryWithChannel(shouldBeInitialized, nil)
}

func FakeLDClientFactoryWithChannel(shouldBeInitialized bool, createdCh chan<- *FakeLDClient) sdks.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		// We're not creating a real client, but we still need to invoke the DataStoreFactory as the
		// SDK would do, since that's how Relay obtains its shared reference to the data store.
		if config.DataStore != nil {
			_, err := config.DataStore.Build(
				subsystems.BasicClientContext{},
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

func RealLDClientFactoryWithChannel(shouldBeInitialized bool, createdCh chan<- CapturedLDClient) sdks.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		c, err := sdks.DefaultClientFactory()(sdkKey, config, timeout)
		if c != nil && createdCh != nil {
			createdCh <- CapturedLDClient{Key: sdkKey, Client: c}
		}
		return c, err
	}
}

func ClientFactoryThatFails(err error) sdks.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config, timeout time.Duration) (sdks.LDClientContext, error) {
		return nil, err
	}
}
