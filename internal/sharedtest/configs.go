package sharedtest

import (
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-server-sdk/v6/subsystems"
)

type singleInstanceConfigurer[T any] struct {
	instance T
}

func (s singleInstanceConfigurer[T]) Build(clientContext subsystems.ClientContext) (T, error) {
	return s.instance, nil
}

func ExistingInstance[T any](instance T) subsystems.ComponentConfigurer[T] {
	return singleInstanceConfigurer[T]{instance}
}

func MakeBasicHTTPConfig() httpconfig.HTTPConfig {
	ret, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, nil, "", ldlog.NewDisabledLoggers())
	if err != nil {
		panic(err)
	}
	return ret
}
