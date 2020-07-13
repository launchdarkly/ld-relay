package relay

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/version"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

func TestRelayStatusEndpoint(t *testing.T) {
	config := c.DefaultConfig
	config.Environment = makeEnvConfigs(testEnvMain, testEnvClientSide, testEnvMobile)

	relayTest(config, func(p relayTestParams) {
		r, _ := http.NewRequest("GET", "http://localhost/status", nil)
		result, body := doRequest(r, p.relay)
		assert.Equal(t, http.StatusOK, result.StatusCode)
		status := ldvalue.Parse(body)

		assertJSONPathMatch(t, "sdk-********-****-****-****-*******e42d0",
			status, "environments", testEnvMain.name, "sdkKey")
		assertJSONPathMatch(t, "connected", status, "environments", testEnvMain.name, "status")

		assertJSONPathMatch(t, "sdk-********-****-****-****-*******e42d1",
			status, "environments", testEnvClientSide.name, "sdkKey")
		assertJSONPathMatch(t, "507f1f77bcf86cd799439011",
			status, "environments", testEnvClientSide.name, "envId")
		assertJSONPathMatch(t, "connected",
			status, "environments", testEnvClientSide.name, "status")

		assertJSONPathMatch(t, "sdk-********-****-****-****-*******e42d2",
			status, "environments", testEnvMobile.name, "sdkKey")
		assertJSONPathMatch(t, "mob-********-****-****-****-*******e42db",
			status, "environments", testEnvMobile.name, "mobileKey")
		assertJSONPathMatch(t, "connected",
			status, "environments", testEnvMobile.name, "status")

		assertJSONPathMatch(t, "healthy", status, "status")
		assertJSONPathMatch(t, version.Version, status, "version")
		assertJSONPathMatch(t, ld.Version, status, "clientVersion")
	})
}
