package relay

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
)

func TestEnvContextInfo(t *testing.T) {
	env := newTestEnvContext("env", true, sharedtest.NewInMemoryStore())
	ec := EnvContextInfo{
		Env: env,
	}

	ctx1 := context.Background()
	ctx2 := WithEnvContextInfo(ctx1, ec)
	assert.Equal(t, ec, GetEnvContextInfo(ctx2))
}
