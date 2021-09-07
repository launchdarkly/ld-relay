package sharedtest

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/httpconfig"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func MakeBasicHTTPConfig() httpconfig.HTTPConfig {
	ret, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDisabledLoggers())
	if err != nil {
		panic(err)
	}
	return ret
}
