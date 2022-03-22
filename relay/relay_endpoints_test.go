package relay

import (
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testsuites"
)

// The tests for standard Relay endpoints are defined in core/coretest, since most of them
// will also be used for Relay Proxy Enterprise.

func relayTestConstructor(config c.Config, loggers ldlog.Loggers) testsuites.TestParams {
	r, err := newRelayInternal(config, relayInternalOptions{
		loggers:       loggers,
		clientFactory: testclient.CreateDummyClient,
	})
	if err != nil {
		panic(err)
	}
	err = r.core.WaitForAllClients(time.Second)
	if err != nil {
		panic(err)
	}
	return testsuites.TestParams{
		Core:    r.core,
		Handler: r.Handler,
		Closer:  func() { r.Close() },
	}
}

func TestRelayEndpoints(t *testing.T) {
	testsuites.DoAllCoreEndpointTests(t, relayTestConstructor)
}
