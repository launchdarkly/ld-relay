package relay

import (
	"testing"

	c "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testsuites"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// The tests for standard Relay endpoints are defined in core/coretest, since most of them
// will also be used for Relay Proxy Enterprise.

func relayTestConstructor(config c.Config) testsuites.TestParams {
	r, err := newRelayInternal(config, ldlog.NewDisabledLoggers(), testclient.CreateDummyClient)
	if err != nil {
		panic(err)
	}
	return testsuites.TestParams{
		Core:    r.core,
		Handler: r.Handler,
		Closer:  func() { r.Close() },
	}
}

func TestRelayCoreEndpoints(t *testing.T) {
	testsuites.DoAllCoreEndpointTests(t, relayTestConstructor)
}
