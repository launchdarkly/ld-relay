package relay

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v6/config"
	st "github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

const (
	serverSideEventsURL = "/bulk"
	mobileEventsURL     = "/mobile/events/bulk"
	jsEventsURL         = "/events/bulk/"
)

func makeEnvWithModifiedSDKKey(e testAutoConfEnv) testAutoConfEnv {
	e.sdkKey += "-changed"
	e.version++
	return e
}

func makeEnvWithModifiedMobileKey(e testAutoConfEnv) testAutoConfEnv {
	e.mobKey += "-changed"
	e.version++
	return e
}

func verifyEventProxying(p autoConfTestParams, url string, authKey config.SDKCredential) {
	verifyEventVerbatimRelay(p, url, authKey)
	verifyEventSummarizingRelay(p, url, authKey)
}

func verifyEventVerbatimRelay(p autoConfTestParams, url string, authKey config.SDKCredential) {
	body := []byte(`[{"kind":"test"}]`)
	headers := make(http.Header)
	headers.Set("X-LaunchDarkly-Event-Schema", "3")
	if authKey.GetAuthorizationHeaderValue() != "" {
		headers.Set("Authorization", authKey.GetAuthorizationHeaderValue())
	}
	req := st.BuildRequest("POST", url, body, headers)

	resp, _ := st.DoRequest(req, p.relay.Handler)
	require.Equal(p.t, 202, resp.StatusCode)

	gotReq := <-p.eventRequestsCh
	assert.Equal(p.t, authKey.GetAuthorizationHeaderValue(), gotReq.Request.Header.Get("Authorization"))
}

func verifyEventSummarizingRelay(p autoConfTestParams, url string, authKey config.SDKCredential) {
	body := []byte(`[{"kind":"feature","timestamp":1000,"key":"flagkey","version":100,"variation":1,"value":"a"}]`)
	headers := make(http.Header)
	if authKey.GetAuthorizationHeaderValue() != "" {
		headers.Set("Authorization", authKey.GetAuthorizationHeaderValue())
	}
	req := st.BuildRequest("POST", url, body, headers)

	resp, _ := st.DoRequest(req, p.relay.Handler)
	require.Equal(p.t, 202, resp.StatusCode)

	gotReq := <-p.eventRequestsCh
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
		assert.Equal(t, modified.sdkKey, client2.Key)

		client1.AwaitClose(t, time.Second)

		p.awaitCredentialsUpdated(env, modified.params())
		assert.Nil(t, p.relay.core.GetEnvironment(testAutoConfEnv1.sdkKey))
	})
}

func TestAutoConfigUpdateEnvironmentSDKKeyWithExpiry(t *testing.T) {
	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)
	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

		modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
		modified.sdkKeyExpiryValue = testAutoConfEnv1.sdkKey
		modified.sdkKeyExpiryTime = ldtime.UnixMillisNow() + 100000
		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.sdkKey, client2.Key)

		p.awaitCredentialsUpdated(env, modified.params())
		p.assertEnvLookup(env, testAutoConfEnv1.params()) // looking up env by old key still works
		assert.Equal(t, []config.SDKCredential{testAutoConfEnv1.sdkKey}, env.GetDeprecatedCredentials())

		select {
		case <-client1.CloseCh:
			require.Fail(t, "should not have closed client for deprecated key yet")
		case <-time.After(time.Millisecond * 300):
			break
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

			verifyEventProxying(p, serverSideEventsURL, modified.sdkKey)
			verifyEventProxying(p, mobileEventsURL, testAutoConfEnv1.mobKey)
			verifyEventProxying(p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})

	t.Run("when some events have been forwarded prior to the change", func(t *testing.T) {
		autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
			env := p.awaitEnvironment(testAutoConfEnv1.id)
			assertEnvProps(t, testAutoConfEnv1.params(), env)

			verifyEventProxying(p, serverSideEventsURL, testAutoConfEnv1.sdkKey)
			verifyEventProxying(p, mobileEventsURL, testAutoConfEnv1.mobKey)

			modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
			p.stream.Enqueue(makeAutoConfPatchEvent(modified))

			p.awaitCredentialsUpdated(env, modified.params())

			verifyEventProxying(p, serverSideEventsURL, modified.sdkKey)
			verifyEventProxying(p, mobileEventsURL, testAutoConfEnv1.mobKey)
			verifyEventProxying(p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})
}

func TestAutoConfigRemovesCredentialForExpiredSDKKey(t *testing.T) {
	briefExpiryMillis := 300
	oldKey := testAutoConfEnv1.sdkKey

	initialEvent := makeAutoConfPutEvent(testAutoConfEnv1)

	autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
		client1 := p.awaitClient()

		env := p.awaitEnvironment(testAutoConfEnv1.id)
		assertEnvProps(t, testAutoConfEnv1.params(), env)

		modified := makeEnvWithModifiedSDKKey(testAutoConfEnv1)
		modified.sdkKeyExpiryValue = oldKey
		modified.sdkKeyExpiryTime = ldtime.UnixMillisNow() + ldtime.UnixMillisecondTime(briefExpiryMillis)
		p.stream.Enqueue(makeAutoConfPatchEvent(modified))

		client2 := p.awaitClient()
		assert.Equal(t, modified.sdkKey, client2.Key)

		p.awaitCredentialsUpdated(env, modified.params())
		newCredentials := credentialsAsSet(env.GetCredentials()...)
		assert.Equal(t, env, p.relay.core.GetEnvironment(oldKey))

		<-time.After(time.Duration(briefExpiryMillis+100) * time.Millisecond)

		select {
		case <-client1.CloseCh:
			break
		case <-time.After(time.Millisecond * 300):
			require.Fail(t, "timed out waiting for client with old key to close")
		}

		assert.Equal(t, newCredentials, credentialsAsSet(env.GetCredentials()...))
		assert.Nil(t, p.relay.core.GetEnvironment(oldKey))
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
		assert.Nil(t, p.relay.core.GetEnvironment(testAutoConfEnv1.mobKey))
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

			verifyEventProxying(p, serverSideEventsURL, testAutoConfEnv1.sdkKey)
			verifyEventProxying(p, mobileEventsURL, modified.mobKey)
			verifyEventProxying(p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})

	t.Run("when some events have been forwarded prior to the change", func(t *testing.T) {
		autoConfTest(t, testAutoConfDefaultConfig, &initialEvent, func(p autoConfTestParams) {
			env := p.awaitEnvironment(testAutoConfEnv1.id)
			assertEnvProps(t, testAutoConfEnv1.params(), env)

			verifyEventVerbatimRelay(p, serverSideEventsURL, testAutoConfEnv1.sdkKey)
			verifyEventVerbatimRelay(p, mobileEventsURL, testAutoConfEnv1.mobKey)

			modified := makeEnvWithModifiedMobileKey(testAutoConfEnv1)
			p.stream.Enqueue(makeAutoConfPatchEvent(modified))

			p.awaitCredentialsUpdated(env, modified.params())

			verifyEventProxying(p, serverSideEventsURL, testAutoConfEnv1.sdkKey)
			verifyEventProxying(p, mobileEventsURL, modified.mobKey)
			verifyEventProxying(p, jsEventsURL+string(testAutoConfEnv1.id), testAutoConfEnv1.id)
		})
	})
}
