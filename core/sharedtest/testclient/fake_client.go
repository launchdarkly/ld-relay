package testclient

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/testhelpers"
)

func CreateDummyClient(sdkKey config.SDKKey, sdkConfig ld.Config) (sdks.LDClientContext, error) {
	store, _ := sdkConfig.DataStore.(*store.SSERelayDataStoreAdapter).CreateDataStore(
		testhelpers.NewSimpleClientContext(string(sdkKey)), nil)
	err := store.Init(sharedtest.AllData)
	if err != nil {
		panic(err)
	}
	return fakeLDClient{true}, nil
}

type fakeLDClient struct {
	initialized bool
}

func (c fakeLDClient) Initialized() bool {
	return c.initialized
}

func (c fakeLDClient) SecureModeHash(user lduser.User) string {
	return FakeHashForUser(user)
}

func FakeHashForUser(user lduser.User) string {
	return "fake-hash-" + user.GetKey()
}

func FakeLDClientFactory(shouldBeInitialized bool) sdks.ClientFactoryFunc {
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
		return fakeLDClient{initialized: shouldBeInitialized}, nil
	}
}

func ClientFactoryThatFails(err error) sdks.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config) (sdks.LDClientContext, error) {
		return nil, err
	}
}
