package testsuites

import (
	"testing"
	"time"

	config "github.com/launchdarkly/ld-relay-config"
	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/core/relayenv"
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
		relayenv.LogNameIsSDKKey,
	)
	if err != nil {
		panic(err)
	}
	core.WaitForAllClients(time.Second)
	return TestParams{
		Core:    core,
		Handler: core.MakeRouter(),
		Closer:  func() { core.Close() },
	}
}

func TestRelayCoreEndpoints(t *testing.T) {
	DoAllCoreEndpointTests(t, relayCoreForEndpointTests)
}
