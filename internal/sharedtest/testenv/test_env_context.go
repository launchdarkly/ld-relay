// Package testenv contains test helpers that reference the relayenv package. These are in sharedtest/testenv
// rather than just sharedtest so that sharedtest can be used by relayenv itself without a circular reference.
package testenv

import (
	"time"

	"github.com/launchdarkly/ld-relay/v7/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v7/internal/sdks"
	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v7/internal/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
)

func NewTestEnvContext(name string, shouldBeInitialized bool, store subsystems.DataStore) relayenv.EnvContext {
	return NewTestEnvContextWithClientFactory(name, testclient.FakeLDClientFactory(shouldBeInitialized), store)
}

func NewTestEnvContextWithClientFactory(
	name string,
	f sdks.ClientFactoryFunc,
	store subsystems.DataStore,
) relayenv.EnvContext {
	dataStoreFactory := ldcomponents.InMemoryDataStore()
	if store != nil {
		dataStoreFactory = sharedtest.ExistingInstance(store)
	}
	readyCh := make(chan relayenv.EnvContext)
	_, err := relayenv.NewEnvContext(relayenv.EnvContextImplParams{
		Identifiers:      relayenv.EnvIdentifiers{ConfiguredName: name},
		ClientFactory:    f,
		DataStoreFactory: dataStoreFactory,
		UserAgent:        "fake-user-agent",
		Loggers:          ldlog.NewDisabledLoggers(),
	}, readyCh)
	if err != nil {
		panic(err)
	}
	if c, ok, _ := helpers.TryReceive(readyCh, time.Second); ok {
		return c
	}
	panic("timed out waiting for client initialization")
}

func MakeTestContextWithData() relayenv.EnvContext {
	return NewTestEnvContext("", true, sharedtest.MakeStoreWithData(true))
}
