package relay

import (
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"

	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	serverSideEventsURL = "/bulk"
	mobileEventsURL     = "/mobile/events/bulk"
	jsEventsURL         = "/events/bulk/"
)

func makeEnvWithModifiedSDKKey(e testAutoConfEnv) testAutoConfEnv {
	e.sdkKey.Value += "-changed"
	e.version++
	return e
}

func makeEnvWithModifiedMobileKey(e testAutoConfEnv) testAutoConfEnv {
	e.mobKey += "-changed"
	e.version++
	return e
}

func verifyEventProxying(t *testing.T, p autoConfTestParams, url string, authKey credential.SDKCredential) {
	verifyEventVerbatimRelay(t, p, url, authKey)
	verifyEventSummarizingRelay(t, p, url, authKey)
}

func verifyEventVerbatimRelay(t *testing.T, p autoConfTestParams, url string, authKey credential.SDKCredential) {
	body := []byte(`[{"kind":"test"}]`)
	headers := make(http.Header)
	headers.Set("X-LaunchDarkly-Event-Schema", "3")
	if authKey.GetAuthorizationHeaderValue() != "" {
		headers.Set("Authorization", authKey.GetAuthorizationHeaderValue())
	}
	req := st.BuildRequest("POST", url, body, headers)

	resp, _ := st.DoRequest(req, p.relay.Handler)
	require.Equal(p.t, 202, resp.StatusCode)

	gotReq := helpers.RequireValue(t, p.eventRequestsCh, time.Second*5)
	assert.Equal(p.t, authKey.GetAuthorizationHeaderValue(), gotReq.Request.Header.Get("Authorization"))
}

func verifyEventSummarizingRelay(t *testing.T, p autoConfTestParams, url string, authKey credential.SDKCredential) {
	body := []byte(`[{"kind":"feature","timestamp":1000,"key":"flagkey","version":100,"variation":1,"value":"a","user":{"key":"u"}}]`)
	headers := make(http.Header)
	if authKey.GetAuthorizationHeaderValue() != "" {
		headers.Set("Authorization", authKey.GetAuthorizationHeaderValue())
	}
	req := st.BuildRequest("POST", url, body, headers)

	resp, _ := st.DoRequest(req, p.relay.Handler)
	require.Equal(p.t, 202, resp.StatusCode)

	gotReq := helpers.RequireValue(t, p.eventRequestsCh, time.Second*5)
	assert.Equal(p.t, authKey.GetAuthorizationHeaderValue(), gotReq.Request.Header.Get("Authorization"))
	eventsValue := ldvalue.Parse(gotReq.Body)
	assert.Equal(p.t, "summary", eventsValue.GetByIndex(eventsValue.Count()-1).GetByKey("kind").StringValue())
}

func TestAutoConfigUpdateEnvironmentSDKKeyWithNoExpiry(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

		modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.SDKKey(), client2.Key)

		client1.AwaitClose(t, 10000*time.Second)

		p.awaitCredentialsUpdated(env, modified.params())
		noEnv, _ := p.relay.getEnvironment(sdkauth.New(testAutoConfEnv1.SDKKey()))
		assert.Nil(t, noEnv)
	})
}

func TestAutoConfigUpdateEnvironmentSDKKeyWithExpiry(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

		modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
		modified.sdkKey.Expiring = envfactory.ExpiringKeyRep{
			Value:     testAutoConfEnv1.SDKKey(),
			Timestamp: ldtime.UnixMillisNow() + 100000,
		}
		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.SDKKey(), client2.Key)

		p.awaitCredentialsUpdated(env, modified.params())
		p.assertEnvLookup(env, testAutoConfEnv1.params()) // looking up env by old key still works
		assert.Equal(t, []credential.SDKCredential{testAutoConfEnv1.sdkKey.Value}, env.GetDeprecatedCredentials())

		if !helpers.AssertChannelNotClosed(t, client1.CloseCh, time.Millisecond*300, "should not have closed client for deprecated key yet") {
			t.FailNow()
		}
	})
}

func TestEventForwardingAfterSDKKeyChange(t *testing.T) {
	// There are two subtests here because event forwarding components are created lazily, so we need to
	// make sure this works correctly if they have or haven't already been created.

	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)

	t.Run("when no events have been forwarded prior to the change", func(t *testing.T) {
		autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
			env := p.awaitEnvironment(testAutoConfEnv1.id)
			assertEnvProps(t, testAutoConfEnv1.params(), env)

			modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
			p.stream.Enqueue(makeAutoConfPatchEvent(modified))

			p.awaitCredentialsUpdated(env, modified.params())

			verifyEventProxying(t, p, serverSideEventsURL, modified.SDKKey())
			verifyEventProxying(t, p, mobileEventsURL, testAutoConfEnv1.mobKey)
			verifyEventProxying(t, p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})

	t.Run("when some events have been forwarded prior to the change", func(t *testing.T) {
		autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
			env := p.awaitEnvironment(testAutoConfEnv1.id)
			assertEnvProps(t, testAutoConfEnv1.params(), env)

			verifyEventProxying(t, p, serverSideEventsURL, testAutoConfEnv1.sdkKey.Value)
			verifyEventProxying(t, p, mobileEventsURL, testAutoConfEnv1.mobKey)

			modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
			p.stream.Enqueue(makeAutoConfPatchEvent(modified))

			p.awaitCredentialsUpdated(env, modified.params())

			verifyEventProxying(t, p, serverSideEventsURL, modified.sdkKey.Value)
			verifyEventProxying(t, p, mobileEventsURL, testAutoConfEnv1.mobKey)
			verifyEventProxying(t, p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})
}

func TestAutoConfigRemovesCredentialForExpiredSDKKey(t *testing.T) {
	briefExpiryMillis := 300
	oldKey := testAutoConfEnv1.sdkKey.Value

	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)

	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

		modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
		modified.sdkKey.Expiring = envfactory.ExpiringKeyRep{
			Value:     oldKey,
			Timestamp: ldtime.UnixMillisNow() + ldtime.UnixMillisecondTime(briefExpiryMillis),
		}
		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.SDKKey(), client2.Key)

		p.awaitCredentialsUpdated(env, modified.params())
		newCredentials := credentialsAsSet(env.GetCredentials()...)
		foundEnvWithOldKey, _ := p.relay.getEnvironment(sdkauth.New(oldKey))
		assert.Equal(t, env, foundEnvWithOldKey)

		if !helpers.AssertChannelClosed(t, client1.CloseCh, time.Duration(briefExpiryMillis+100)*time.Millisecond, "timed out waiting for client with old key to close") {
			t.FailNow()
		}

		assert.Equal(t, newCredentials, credentialsAsSet(env.GetCredentials()...))
		noEnv, _ := p.relay.getEnvironment(sdkauth.New(oldKey))
		assert.Nil(t, noEnv)
	})
}

func TestAutoConfigUpdateEnvironmentMobileKey(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		_ = p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

		modified := makeEnvWithModifiedMobileKey(testAutoConfEnv1)
		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		p.shouldNotCreateClient(time.Millisecond * 50)

		p.awaitCredentialsUpdated(env, modified.params())
		noEnv, _ := p.relay.getEnvironment(sdkauth.New(testAutoConfEnv1.mobKey))
		assert.Nil(t, noEnv)
	})
}

func TestEventForwardingAfterMobileKeyChange(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)

	t.Run("when no events have been forwarded prior to the change", func(t *testing.T) {
		autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
			env := p.awaitEnvironment(testAutoConfEnv1.id)
			assertEnvProps(t, testAutoConfEnv1.params(), env)

			modified := makeEnvWithModifiedMobileKey(testAutoConfEnv1)
			p.stream.Enqueue(makeAutoConfPatchEvent(modified))

			p.awaitCredentialsUpdated(env, modified.params())

			verifyEventProxying(t, p, serverSideEventsURL, testAutoConfEnv1.sdkKey.Value)
			verifyEventProxying(t, p, mobileEventsURL, modified.mobKey)
			verifyEventProxying(t, p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})

	t.Run("when some events have been forwarded prior to the change", func(t *testing.T) {
		autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
			env := p.awaitEnvironment(testAutoConfEnv1.id)
			assertEnvProps(t, testAutoConfEnv1.params(), env)

			verifyEventVerbatimRelay(t, p, serverSideEventsURL, testAutoConfEnv1.sdkKey.Value)
			verifyEventVerbatimRelay(t, p, mobileEventsURL, testAutoConfEnv1.mobKey)

			modified := makeEnvWithModifiedMobileKey(testAutoConfEnv1)
			p.stream.Enqueue(makeAutoConfPatchEvent(modified))

			p.awaitCredentialsUpdated(env, modified.params())

			verifyEventProxying(t, p, serverSideEventsURL, testAutoConfEnv1.sdkKey.Value)
			verifyEventProxying(t, p, mobileEventsURL, modified.mobKey)
			verifyEventProxying(t, p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})
}
