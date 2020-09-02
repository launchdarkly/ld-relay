package middleware

import (
	"context"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/core/sharedtest/testenv"

	"github.com/stretchr/testify/assert"
)

func TestEnvContextInfo(t *testing.T) {
	env := testenv.NewTestEnvContext("env", true, sharedtest.NewInMemoryStore())
	ec := EnvContextInfo{
		Env: env,
	}

	ctx1 := context.Background()
	ctx2 := WithEnvContextInfo(ctx1, ec)
	assert.Equal(t, ec, GetEnvContextInfo(ctx2))
}
