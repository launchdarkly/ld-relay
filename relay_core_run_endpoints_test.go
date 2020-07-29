package relay

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

// The test suites in coretest will be run for all Relay distributions, so that we can be sure that the
// core components have been set up correctly for each distribution. But we also want to make sure that
// the test suites are correct with regard to the core components alone, so we run them here too.
//
// This file is in the coretest package rather than the core package because otherwise there would be a
// circular package reference.

func relayCoreForEndpointTests(c config.Config) TestParams {
	core, err := NewRelayCore(c, ldlog.NewDisabledLoggers(), testclient.CreateDummyClient)
	if err != nil {
		panic(err)
	}
	return TestParams{
		Core:    core,
		Handler: core.MakeRouter(),
		Closer:  func() { core.Close() },
	}
}

func TestRelayCoreEndpoints(t *testing.T) {
	DoAllCoreEndpointTests(t, relayCoreForEndpointTests)
}
