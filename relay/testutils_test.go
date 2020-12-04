package relay

import (
	"reflect"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/envfactory"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type relayTestHelper struct {
	t     *testing.T
	relay *Relay
}

func (h relayTestHelper) awaitEnvironment(envID c.EnvironmentID) relayenv.EnvContext {
	var e relayenv.EnvContext
	require.Eventually(h.t, func() bool {
		e, _ = h.relay.core.GetEnvironment(envID)
		return e != nil
	}, time.Second, time.Millisecond*5)
	return e
}

func (h relayTestHelper) shouldNotHaveEnvironment(envID c.EnvironmentID, timeout time.Duration) {
	require.Eventually(h.t, func() bool {
		e, _ := h.relay.core.GetEnvironment(envID)
		return e == nil
	}, timeout, time.Millisecond*5)
}

func (h relayTestHelper) assertEnvLookup(env relayenv.EnvContext, expected envfactory.EnvironmentParams) {
	foundEnv, _ := h.relay.core.GetEnvironment(expected.EnvID)
	assert.Equal(h.t, env, foundEnv)
	foundEnv, _ = h.relay.core.GetEnvironment(expected.MobileKey)
	assert.Equal(h.t, env, foundEnv)
	foundEnv, _ = h.relay.core.GetEnvironment(expected.SDKKey)
	assert.Equal(h.t, env, foundEnv)
}

func (h relayTestHelper) awaitCredentialsUpdated(env relayenv.EnvContext, expected envfactory.EnvironmentParams) {
	expectedCredentials := credentialsAsSet(expected.EnvID, expected.MobileKey, expected.SDKKey)
	isChanged := func() bool {
		return reflect.DeepEqual(credentialsAsSet(env.GetCredentials()...), expectedCredentials)
	}
	require.Eventually(h.t, isChanged, time.Second, time.Millisecond*5)
	h.assertEnvLookup(env, expected)
}

func assertEnvProps(t *testing.T, expected envfactory.EnvironmentParams, env relayenv.EnvContext) {
	assert.Equal(t, credentialsAsSet(expected.EnvID, expected.MobileKey, expected.SDKKey),
		credentialsAsSet(env.GetCredentials()...))
	assert.Equal(t, expected.Identifiers, env.GetIdentifiers())
	assert.Equal(t, expected.Identifiers.ProjName+" "+expected.Identifiers.EnvName,
		env.GetIdentifiers().GetDisplayName())
}

func credentialsAsSet(cs ...c.SDKCredential) map[c.SDKCredential]struct{} {
	ret := make(map[c.SDKCredential]struct{}, len(cs))
	for _, c := range cs {
		ret[c] = struct{}{}
	}
	return ret
}
