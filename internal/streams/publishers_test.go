package streams

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/eventsource"
)

func TestNewPublishers(t *testing.T) {
	maxConnTime := time.Minute

	ps := NewPublishers(maxConnTime)
	require.NotNil(t, ps)
	defer ps.Close()

	verifyServer := func(p Publisher) {
		require.NotNil(t, p)
		require.IsType(t, &eventsource.Server{}, p)
		s := p.(*eventsource.Server)
		assert.False(t, s.Gzip)
		assert.True(t, s.AllowCORS)
		assert.True(t, s.ReplayAll)
		assert.Equal(t, maxConnTime, s.MaxConnTime)
	}

	verifyServer(ps.ServerSideAll)
	verifyServer(ps.ServerSideFlags)
	verifyServer(ps.Mobile)
	verifyServer(ps.JSClient)
}

func TestClosePublishers(t *testing.T) {
	ps := NewPublishers(time.Hour)
	require.NotNil(t, ps)

	finished := make(chan struct{}, 4)
	startRequest := func(p Publisher, channel string) {
		go func() {
			req, _ := http.NewRequest("GET", "", nil)
			rr := &httptest.ResponseRecorder{}
			p.(*eventsource.Server).Handler(channel).ServeHTTP(rr, req)
			finished <- struct{}{}
		}()
	}

	startRequest(ps.ServerSideAll, string(testSDKKey))
	startRequest(ps.ServerSideFlags, string(testSDKKey))
	startRequest(ps.Mobile, string(testMobileKey))
	startRequest(ps.JSClient, string(testEnvID))

	select {
	case <-finished:
		require.Fail(t, "client stream was closed too soon")
	case <-time.After(time.Millisecond * 50):
		break
	}

	ps.Close()

	numFinished := 0
	for {
		select {
		case <-finished:
			numFinished++
			if numFinished == 4 {
				return
			}
		case <-time.After(time.Millisecond * 100):
			require.Fail(t, "timed out waiting for client streams to be closed")
		}
	}
}
