package relay

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"
	c "github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

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
		e, _ = h.relay.getEnvironment(envID)
		return e != nil
	}, time.Second, time.Millisecond*5)
	return e
}

func (h relayTestHelper) shouldNotHaveEnvironment(envID c.EnvironmentID, timeout time.Duration) {
	require.Eventually(h.t, func() bool {
		e, _ := h.relay.getEnvironment(envID)
		return e == nil
	}, timeout, time.Millisecond*5)
}

func (h relayTestHelper) assertEnvLookup(env relayenv.EnvContext, expected envfactory.EnvironmentParams) {
	foundEnv, _ := h.relay.getEnvironment(expected.EnvID)
	assert.Equal(h.t, env, foundEnv)
	foundEnv, _ = h.relay.getEnvironment(expected.MobileKey)
	assert.Equal(h.t, env, foundEnv)
	foundEnv, _ = h.relay.getEnvironment(expected.SDKKey)
	assert.Equal(h.t, env, foundEnv)

	h.assertSDKEndpointsAvailability(true, expected.SDKKey, expected.MobileKey, expected.EnvID)
}

func (h relayTestHelper) assertSDKEndpointsAvailability(
	shouldBeAvailable bool,
	sdkKey config.SDKKey,
	mobileKey config.MobileKey,
	envID config.EnvironmentID,
) {
	// Here we're making sure that all of the SDK endpoints are properly recognizing the appropriate
	// authorized credentials (if shouldBeAvailable is true) or rejecting unauthorized credentials (if
	// shouldBeAvailable is false). We're not checking the response body, just that we get the
	// appropriate HTTP status. These are tested more thoroughly in test suites like DoStreamEndpointsTests,
	// but we use this simpler test when we're dynamically changing what the credentials are.

	simpleUserJSON := []byte(`{"key":"userkey"}`)
	simpleUserBase64 := "eyJrZXkiOiJ1c2Vya2V5In0="
	status200Or401, status200Or404 := 200, 200
	if !shouldBeAvailable {
		status200Or401 = 401
		status200Or404 = 404
	}
	if sdkKey != "" {
		h.assertEndpointStatus(status200Or401, "GET", "/all", sdkKey, nil)
		h.assertEndpointStatus(status200Or401, "GET", "/flags", sdkKey, nil)
		h.assertEndpointStatus(status200Or401, "REPORT", "/sdk/evalx/context", sdkKey, simpleUserJSON)
	}
	if mobileKey != "" {
		h.assertEndpointStatus(status200Or401, "GET", "/mping", mobileKey, nil)
		h.assertEndpointStatus(status200Or401, "GET", "/meval/"+simpleUserBase64, mobileKey, nil)
		h.assertEndpointStatus(status200Or401, "REPORT", "/meval", mobileKey, simpleUserJSON)
	}
	if envID != "" {
		h.assertEndpointStatus(status200Or404, "GET", "/ping/"+string(envID), nil, nil)
		h.assertEndpointStatus(status200Or404, "GET", "/eval/"+string(envID)+"/"+simpleUserBase64, nil, nil)
		h.assertEndpointStatus(status200Or404, "REPORT", "/eval/"+string(envID), nil, simpleUserJSON)
	}
}

func (h relayTestHelper) assertEndpointStatus(
	expectedStatus int,
	method, path string,
	authKey config.SDKCredential,
	body []byte,
) {
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	headers := make(http.Header)
	var authValue string
	if authKey != nil {
		authValue = authKey.GetAuthorizationHeaderValue()
	}
	if authValue != "" {
		headers.Add("Authorization", authValue)
	}
	if body != nil {
		headers.Add("Content-Type", "application/json")
	}
	req := sharedtest.BuildRequest(method, path, body, headers).WithContext(ctx)
	status := sharedtest.CallHandlerAndAwaitStatus(h.t, h.relay, req, time.Second)
	require.Equal(h.t, expectedStatus, status, "expected status %d but got %d from %s %s with auth key %s",
		expectedStatus, status, method, path, authValue)
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
