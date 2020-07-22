package relay

import (
	"time"

	"github.com/launchdarkly/eventsource"
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
	"github.com/launchdarkly/ld-relay/v6/sdkconfig"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

var emptyStore = sharedtest.NewInMemoryStore()
var emptyStoreAdapter = store.NewSSERelayDataStoreAdapterWithExistingStore(emptyStore)

type testEnvironments map[config.SDKCredential]relayenv.EnvContext

func (t testEnvironments) GetEnvironment(c config.SDKCredential) relayenv.EnvContext {
	return t[c]
}

func (t testEnvironments) GetAllEnvironments() map[config.SDKKey]relayenv.EnvContext {
	ret := make(map[config.SDKKey]relayenv.EnvContext)
	for k, v := range t {
		if sk, ok := k.(config.SDKKey); ok {
			ret[sk] = v
		}
	}
	return ret
}

func clientFactoryThatFails(err error) sdkconfig.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config) (sdkconfig.LDClientContext, error) {
		return nil, err
	}
}

type fakeLDClient struct {
	initialized bool
}

func (c fakeLDClient) Initialized() bool {
	return c.initialized
}

func (c fakeLDClient) SecureModeHash(user lduser.User) string {
	return fakeHashForUser(user)
}

func fakeHashForUser(user lduser.User) string {
	return "fake-hash-" + user.GetKey()
}

func fakeLDClientFactory(shouldBeInitialized bool) sdkconfig.ClientFactoryFunc {
	return func(sdkKey config.SDKKey, config ld.Config) (sdkconfig.LDClientContext, error) {
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

type existingDataStoreFactory struct {
	instance interfaces.DataStore
}

func (f existingDataStoreFactory) CreateDataStore(
	interfaces.ClientContext,
	interfaces.DataStoreUpdates,
) (interfaces.DataStore, error) {
	return f.instance, nil
}

func newTestEnvContext(name string, shouldBeInitialized bool, store interfaces.DataStore) relayenv.EnvContext {
	return newTestEnvContextWithClientFactory(name, fakeLDClientFactory(shouldBeInitialized), store)
}

func newTestEnvContextWithClientFactory(
	name string,
	f sdkconfig.ClientFactoryFunc,
	store interfaces.DataStore,
) relayenv.EnvContext {
	dataStoreFactory := ldcomponents.InMemoryDataStore()
	if store != nil {
		dataStoreFactory = existingDataStoreFactory{instance: store}
	}
	fakeServer := eventsource.NewServer()

	readyCh := make(chan relayenv.EnvContext)
	c, err := relayenv.NewEnvContext(
		name,
		config.EnvConfig{},
		config.Config{},
		f,
		dataStoreFactory,
		fakeServer,
		fakeServer,
		fakeServer,
		nil,
		ldlog.NewDisabledLoggers(),
		readyCh,
	)
	if err != nil {
		panic(err)
	}
	select {
	case <-readyCh:
		return c
	case <-time.After(time.Second):
		panic("timed out waiting for client initialization")
	}
}

func makeTestContextWithData() relayenv.EnvContext {
	return newTestEnvContext("", true, makeStoreWithData(true))
}

func makeStoreWithData(initialized bool) interfaces.DataStore {
	store := sharedtest.NewInMemoryStore()
	addAllFlags(store, initialized)
	return store
}
