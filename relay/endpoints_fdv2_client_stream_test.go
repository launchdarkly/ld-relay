package relay

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/eventsource"
	ct "github.com/launchdarkly/go-configtypes"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFDv2ClientSideStreamingWithMobileKey(t *testing.T) {
	env := st.EnvWithAllCredentials
	mobileKey := env.Config.MobileKey
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)
	config.Main.MaxClientConnectionTime = ct.OptDuration{}

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("GET with context in URL", func(t *testing.T) {
			req := st.BuildRequestWithAuth("GET", "/sdk/stream/eval/"+contextBase64, mobileKey, nil)
			st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				event := helpers.RequireValue(t, eventCh, 3*time.Second, "timed out waiting for event")
				require.NotNil(t, event)
				assert.Equal(t, "ping", event.Event())
			})
		})

		t.Run("POST with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(mobileKey))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("POST", "/sdk/stream/eval", contextJSON, h)
			st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				event := helpers.RequireValue(t, eventCh, 3*time.Second, "timed out waiting for event")
				require.NotNil(t, event)
				assert.Equal(t, "ping", event.Event())
			})
		})

		t.Run("auth via query param", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/stream/eval/"+contextBase64+"?auth="+string(mobileKey), nil, nil)
			st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				event := helpers.RequireValue(t, eventCh, 3*time.Second, "timed out waiting for event")
				require.NotNil(t, event)
				assert.Equal(t, "ping", event.Event())
			})
		})

		t.Run("invalid credential returns 401", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := st.BuildRequestWithAuth("GET", "/sdk/stream/eval/"+contextBase64, st.UndefinedMobileKey, nil).WithContext(ctx)
			status := st.CallHandlerAndAwaitStatus(t, p.relay, req, time.Second)
			assert.Equal(t, http.StatusUnauthorized, status)
		})

		t.Run("no credential returns 401", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := st.BuildRequest("GET", "/sdk/stream/eval/"+contextBase64, nil, nil).WithContext(ctx)
			status := st.CallHandlerAndAwaitStatus(t, p.relay, req, time.Second)
			assert.Equal(t, http.StatusUnauthorized, status)
		})
	})
}

func TestFDv2ClientSideStreamingWithEnvironmentID(t *testing.T) {
	env := st.EnvWithAllCredentials
	envID := env.Config.EnvID
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)
	config.Main.MaxClientConnectionTime = ct.OptDuration{}

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("GET with context in URL via header", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			req := st.BuildRequest("GET", "/sdk/stream/eval/"+contextBase64, nil, h)
			st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				event := helpers.RequireValue(t, eventCh, 3*time.Second, "timed out waiting for event")
				require.NotNil(t, event)
				assert.Equal(t, "ping", event.Event())
			})
		})

		t.Run("POST with context in body", func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", string(envID))
			h.Set("Content-Type", "application/json")
			req := st.BuildRequest("POST", "/sdk/stream/eval", contextJSON, h)
			st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				event := helpers.RequireValue(t, eventCh, 3*time.Second, "timed out waiting for event")
				require.NotNil(t, event)
				assert.Equal(t, "ping", event.Event())
			})
		})

		t.Run("auth via query param", func(t *testing.T) {
			req := st.BuildRequest("GET", "/sdk/stream/eval/"+contextBase64+"?auth="+string(envID), nil, nil)
			st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
				event := helpers.RequireValue(t, eventCh, 3*time.Second, "timed out waiting for event")
				require.NotNil(t, event)
				assert.Equal(t, "ping", event.Event())
			})
		})

		t.Run("invalid credential returns 401", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := make(http.Header)
			h.Set("Authorization", string(st.UndefinedEnvID))
			req := st.BuildRequest("GET", "/sdk/stream/eval/"+contextBase64, nil, h).WithContext(ctx)
			status := st.CallHandlerAndAwaitStatus(t, p.relay, req, time.Second)
			assert.Equal(t, http.StatusUnauthorized, status)
		})
	})
}

func TestFDv2ClientSideStreamingConnectionTimeLimit(t *testing.T) {
	env := st.EnvWithAllCredentials
	mobileKey := env.Config.MobileKey
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	maxConnTime := 100 * time.Millisecond
	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)
	config.Main.MaxClientConnectionTime = ct.NewOptDuration(maxConnTime)

	withStartedRelay(t, config, func(p relayTestParams) {
		req := st.BuildRequestWithAuth("GET", "/sdk/stream/eval/"+contextBase64, mobileKey, nil)
		maxWait := time.NewTimer(maxConnTime + time.Second)
		defer maxWait.Stop()
		startTime := time.Now()
		st.WithStreamRequest(t, req, p.relay, func(eventCh <-chan eventsource.Event) {
			for {
				select {
				case event := <-eventCh:
					if event == nil { // stream closed
						timeUntilClosed := time.Since(startTime)
						if timeUntilClosed < maxConnTime {
							assert.Fail(t, "stream closed too soon", "expected %s but closed after %s",
								maxConnTime, timeUntilClosed)
						}
						return
					}
				case <-maxWait.C:
					assert.Fail(t, "timed out waiting for stream to close")
					return
				}
			}
		})
	})
}

func TestFDv2ClientSideStreamingCORSPreflight(t *testing.T) {
	env := st.EnvWithAllCredentials
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("OPTIONS on GET path succeeds without auth", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.relay, "http://localhost/sdk/stream/eval/"+contextBase64, "GET")
		})

		t.Run("OPTIONS on POST path succeeds without auth", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.relay, "http://localhost/sdk/stream/eval", "POST")
		})
	})
}

func TestFDv2ClientSidePollingCORSPreflight(t *testing.T) {
	env := st.EnvWithAllCredentials
	contextJSON := []byte(`{"kind":"user","key":"me"}`)
	contextBase64 := base64.StdEncoding.EncodeToString(contextJSON)

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)

	withStartedRelay(t, config, func(p relayTestParams) {
		t.Run("OPTIONS on GET path succeeds without auth", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.relay, "http://localhost/sdk/poll/eval/"+contextBase64, "GET")
		})

		t.Run("OPTIONS on POST path succeeds without auth", func(t *testing.T) {
			st.AssertEndpointSupportsOptionsRequest(t, p.relay, "http://localhost/sdk/poll/eval", "POST")
		})
	})
}
