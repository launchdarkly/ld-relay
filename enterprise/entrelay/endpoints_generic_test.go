package entrelay

import (
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testsuites"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// The tests for standard Relay endpoints are defined in sharedtest/testendpoints. Currently, Relay Proxy
// Enterprise does not have any additional endpoints.

func relayForEndpointTests(config c.Config) testsuites.TestParams {
	entConfig := config
	r, err := NewRelayEnterprise(entConfig, ldlog.NewDisabledLoggers(), testclient.CreateDummyClient)
	if err != nil {
		panic(err)
	}
	return testsuites.TestParams{
		Core:    r.core,
		Handler: r.handler,
		Closer:  func() { r.Close() },
	}
}

func TestRelayEnterpriseCoreEndpoints(t *testing.T) {
	testsuites.DoAllCoreEndpointTests(t, relayForEndpointTests)
}
