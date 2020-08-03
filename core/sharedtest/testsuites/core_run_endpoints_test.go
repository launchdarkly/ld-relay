package testsuites

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/core/config"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testclient"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const (
	fakeRelayCoreVersion = "9.9.9"
	fakeRelayUserAgent   = "fake-user-agent"
)

func relayCoreForEndpointTests(c config.Config) TestParams {
	core, err := core.NewRelayCore(
		c,
		ldlog.NewDisabledLoggers(),
		testclient.CreateDummyClient,
		fakeRelayCoreVersion,
		fakeRelayUserAgent,
	)
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
