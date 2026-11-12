package relay

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/launchdarkly/eventsource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test proves the wiring between the [Concurrency] configuration and the endpoints.
// The limiter, the middleware, and the stream producer have their own tests, but only a
// request through a started relay shows that the budget is connected to each gated route.
// A relay with the wiring removed passes every package test; this test is what fails.
func TestInitConcurrencyGatesEndpointsEndToEnd(t *testing.T) {
	sdkKey := st.EnvMain.Config.SDKKey

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain)
	config.Concurrency = c.ConcurrencyConfig{
		MaxConcurrent: optInt(1),
		MaxQueued:     optInt(0),
	}

	flagsReq := func() *http.Request {
		return (endpointTestParams{"flags", "GET", "/sdk/flags", nil, sdkKey, 0, st.ExpectNoBody()}).request()
	}
	pollReq := func() *http.Request {
		return (endpointTestParams{"poll", "GET", "/sdk/poll?basis=not-current", nil, sdkKey, 0, st.ExpectNoBody()}).request()
	}
	latestAllReq := func() *http.Request {
		return (endpointTestParams{"latest-all", "GET", "/sdk/latest-all", nil, sdkKey, 0, st.ExpectNoBody()}).request()
	}
	streamReq := func() *http.Request {
		return (endpointTestParams{"stream", "GET", "/sdk/stream", nil, sdkKey, 0, st.ExpectNoBody()}).request()
	}

	withStartedRelay(t, config, func(p relayTestParams) {
		limiter := p.relay.initConcurrency.limiter
		require.True(t, limiter.Enabled(), "the configuration must construct an enabled limiter")

		// Learn the current Etag while the budget is free, for the 304 case below.
		etagResp, _ := st.DoRequest(latestAllReq(), p.relay)
		require.Equal(t, http.StatusOK, etagResp.StatusCode)
		currentEtag := etagResp.Header.Get("Etag")
		require.NotEmpty(t, currentEtag)

		// Hold the only slot, so every gated route must shed.
		hold, ok := limiter.Acquire(context.Background())
		require.True(t, ok)

		t.Run("PHP all-flags poll sheds while the budget is full", func(t *testing.T) {
			resp, body := st.DoRequest(flagsReq(), p.relay)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, string(body), "concurrency limit reached")
		})

		t.Run("FDv2 server poll sheds a full transfer while the budget is full", func(t *testing.T) {
			resp, body := st.DoRequest(pollReq(), p.relay)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, string(body), "concurrency limit reached")
		})

		t.Run("FDv1 server poll sheds a full transfer while the budget is full", func(t *testing.T) {
			resp, body := st.DoRequest(latestAllReq(), p.relay)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, string(body), "concurrency limit reached")
		})

		t.Run("FDv1 server poll answers an unchanged poll while the budget is full", func(t *testing.T) {
			r := latestAllReq()
			r.Header.Set("If-None-Match", currentEtag)
			resp, _ := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusNotModified, resp.StatusCode)
		})

		t.Run("FDv2 server stream sheds the replay while the budget is full", func(t *testing.T) {
			// A shed replay closes the connection, so the handler returns on its own.
			// An admitted replay keeps the stream open until the client leaves, so a
			// prompt return is the signature of the shed. The recorder writes into a
			// pipe, so a reader must drain it or the handler blocks on the write.
			w, bodyReader := st.NewStreamRecorder()
			go func() { _, _ = io.Copy(io.Discard, bodyReader) }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handlerDone := make(chan struct{})
			go func() {
				p.relay.ServeHTTP(w, streamReq().WithContext(ctx))
				close(handlerDone)
			}()
			select {
			case <-handlerDone:
			case <-time.After(3 * time.Second):
				require.Fail(t, "the stream was not shed: the handler did not return while the budget was full")
			}
		})

		// The budget frees, and every route serves again.
		hold()

		t.Run("PHP all-flags poll is served once a slot is free", func(t *testing.T) {
			resp, _ := st.DoRequest(flagsReq(), p.relay)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})

		t.Run("FDv2 server poll is served once a slot is free", func(t *testing.T) {
			resp, _ := st.DoRequest(pollReq(), p.relay)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})

		t.Run("FDv1 server poll is served once a slot is free", func(t *testing.T) {
			resp, _ := st.DoRequest(latestAllReq(), p.relay)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})

		t.Run("FDv2 server stream delivers data once a slot is free", func(t *testing.T) {
			st.WithStreamRequest(t, streamReq(), p.relay, func(eventCh <-chan eventsource.Event) {
				// Drain the whole basis: the event channel has a small buffer, and an
				// undrained stream blocks the handler behind it.
				deadline := time.After(3 * time.Second)
				var got []string
				for {
					select {
					case ev := <-eventCh:
						require.NotNil(t, ev, "the stream closed before the basis was transferred (events: %v)", got)
						got = append(got, ev.Event())
						if ev.Event() == "payload-transferred" {
							assert.Equal(t, "server-intent", got[0])
							return
						}
					case <-deadline:
						require.Failf(t, "timeout", "did not receive the full basis (events: %v)", got)
					}
				}
			})
		})

		// The served requests returned their slots: nothing leaked.
		deadline := time.Now().Add(2 * time.Second)
		for limiter.Stats().Held != 0 {
			if time.Now().After(deadline) {
				require.Failf(t, "slot leak", "slots still held after all requests finished (stats: %+v)", limiter.Stats())
			}
			time.Sleep(time.Millisecond)
		}
	})
}

// A poll whose If-None-Match matches the current Etag gets a 304 reply, and that reply
// must not need a slot: with the budget full, the SDK still learns that its data is current.
// A poll for data the SDK does not have is still shed.
func TestInitConcurrencyAnswersUnchangedFDv2PollsWhileTheBudgetIsFull(t *testing.T) {
	env := st.EnvWithAllCredentials
	sdkKey := env.Config.SDKKey
	mobileKey := env.Config.MobileKey
	contextBase64 := base64.StdEncoding.EncodeToString([]byte(`{"kind":"user","key":"me"}`))

	var config c.Config
	config.Environment = st.MakeEnvConfigs(env)
	config.Concurrency = c.ConcurrencyConfig{
		MaxConcurrent: optInt(1),
		MaxQueued:     optInt(0),
	}

	pollReq := func() *http.Request {
		return (endpointTestParams{"poll", "GET", "/sdk/poll", nil, sdkKey, 0, st.ExpectNoBody()}).request()
	}
	evalReq := func() *http.Request {
		return st.BuildRequestWithAuth("GET", "/sdk/poll/eval/"+contextBase64, mobileKey, nil)
	}

	withStartedRelay(t, config, func(p relayTestParams) {
		limiter := p.relay.initConcurrency.limiter
		require.True(t, limiter.Enabled(), "the configuration must construct an enabled limiter")

		// Learn the current Etags while the budget is free.
		pollResp, _ := st.DoRequest(pollReq(), p.relay)
		require.Equal(t, http.StatusOK, pollResp.StatusCode)
		pollEtag := pollResp.Header.Get("Etag")
		require.NotEmpty(t, pollEtag)

		evalResp, _ := st.DoRequest(evalReq(), p.relay)
		require.Equal(t, http.StatusOK, evalResp.StatusCode)
		evalEtag := evalResp.Header.Get("Etag")
		require.NotEmpty(t, evalEtag)

		// Hold the only slot.
		hold, ok := limiter.Acquire(context.Background())
		require.True(t, ok)
		defer hold()

		t.Run("FDv2 server poll answers an unchanged poll", func(t *testing.T) {
			r := pollReq()
			r.Header.Set("If-None-Match", pollEtag)
			resp, _ := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusNotModified, resp.StatusCode)
		})

		t.Run("FDv2 server poll sheds a poll with a stale Etag", func(t *testing.T) {
			r := pollReq()
			r.Header.Set("If-None-Match", `W/"stale"`)
			resp, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, string(body), "concurrency limit reached")
		})

		t.Run("FDv2 client-side poll answers an unchanged poll", func(t *testing.T) {
			r := evalReq()
			r.Header.Set("If-None-Match", evalEtag)
			resp, _ := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusNotModified, resp.StatusCode)
		})

		t.Run("FDv2 client-side poll sheds a poll with a stale Etag", func(t *testing.T) {
			r := evalReq()
			r.Header.Set("If-None-Match", `W/"stale"`)
			resp, body := st.DoRequest(r, p.relay)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, string(body), "concurrency limit reached")
		})
	})
}
