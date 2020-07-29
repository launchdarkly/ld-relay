// Package testenv contains test helpers that reference the relayenv package. These are in sharedtest/testenv
// rather than just sharedtest so that sharedtest can be used by relayenv itself without a circular reference.
package testenv

import (
	"time"

	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents"
)

func NewTestEnvContext(name string, shouldBeInitialized bool, store interfaces.DataStore) relayenv.EnvContext {
	return NewTestEnvContextWithClientFactory(name, testclient.FakeLDClientFactory(shouldBeInitialized), store)
}

func NewTestEnvContextWithClientFactory(
	name string,
	f sdks.ClientFactoryFunc,
	store interfaces.DataStore,
) relayenv.EnvContext {
	dataStoreFactory := ldcomponents.InMemoryDataStore()
	if store != nil {
		dataStoreFactory = sharedtest.ExistingDataStoreFactory{Instance: store}
	}
	readyCh := make(chan relayenv.EnvContext)
	c, err := relayenv.NewEnvContext(
		name,
		config.EnvConfig{},
		config.Config{},
		f,
		dataStoreFactory,
		nil, //streamProviders,
		relayenv.JSClientContext{},
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

func MakeTestContextWithData() relayenv.EnvContext {
	return NewTestEnvContext("", true, sharedtest.MakeStoreWithData(true))
}
