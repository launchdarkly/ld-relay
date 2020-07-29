package relay

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/store"
)

var emptyStore = sharedtest.NewInMemoryStore()
var emptyStoreAdapter = store.NewSSERelayDataStoreAdapter(sharedtest.ExistingDataStoreFactory{emptyStore}, nil)

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
