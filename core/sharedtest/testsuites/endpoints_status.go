package testsuites

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/ld-relay/v6/core"
	c "github.com/launchdarkly/ld-relay/v6/core/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	ld "gopkg.in/launchdarkly/go-server-sdk.v5"
)

func DoStatusEndpointTest(t *testing.T, constructor TestConstructor) {
	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvClientSide, st.EnvMobile)

	DoTest(config, constructor, func(p TestParams) {
		r, _ := http.NewRequest("GET", "http://localhost/status", nil)
		result, body := st.DoRequest(r, p.Handler)
		assert.Equal(t, http.StatusOK, result.StatusCode)
		status := ldvalue.Parse(body)

		st.AssertJSONPathMatch(t, core.ObscureKey(string(st.EnvMain.Config.SDKKey)),
			status, "environments", st.EnvMain.Name, "sdkKey")
		st.AssertJSONPathMatch(t, "connected", status, "environments", st.EnvMain.Name, "status")

		st.AssertJSONPathMatch(t, core.ObscureKey(string(st.EnvClientSide.Config.SDKKey)),
			status, "environments", st.EnvClientSide.Name, "sdkKey")
		st.AssertJSONPathMatch(t, "507f1f77bcf86cd799439011",
			status, "environments", st.EnvClientSide.Name, "envId")
		st.AssertJSONPathMatch(t, "connected",
			status, "environments", st.EnvClientSide.Name, "status")

		st.AssertJSONPathMatch(t, core.ObscureKey(string(st.EnvMobile.Config.SDKKey)),
			status, "environments", st.EnvMobile.Name, "sdkKey")
		st.AssertJSONPathMatch(t, core.ObscureKey(string(st.EnvMobile.Config.MobileKey)),
			status, "environments", st.EnvMobile.Name, "mobileKey")
		st.AssertJSONPathMatch(t, "connected",
			status, "environments", st.EnvMobile.Name, "status")

		st.AssertJSONPathMatch(t, "healthy", status, "status")
		st.AssertJSONPathMatch(t, p.Core.Version, status, "version")
		st.AssertJSONPathMatch(t, ld.Version, status, "clientVersion")
	})
}
