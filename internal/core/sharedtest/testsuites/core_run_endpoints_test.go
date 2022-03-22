package testsuites

import (
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testclient"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

const (
	fakeRelayCoreVersion = "9.9.9"
	fakeRelayUserAgent   = "fake-user-agent"
)

func relayCoreForEndpointTests(c config.Config, loggers ldlog.Loggers) TestParams {
	core, err := core.NewRelayCore(
		c,
		loggers,
		testclient.CreateDummyClient,
		fakeRelayCoreVersion,
		fakeRelayUserAgent,
		relayenv.LogNameIsSDKKey,
	)
	if err != nil {
		panic(err)
	}
	err = core.WaitForAllClients(time.Second)
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
