package relay

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	ct "github.com/launchdarkly/go-configtypes"

	c "github.com/launchdarkly/ld-relay/v9/config"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadlineRecorder is a ResponseWriter that records the write deadlines set on it.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineRecorder) armed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dl := range d.deadlines {
		if !dl.IsZero() {
			return true
		}
	}
	return false
}

// This test proves the wiring between the Main.ClientWrite* options and the poll and
// evaluation routes: each covered response is written under a deadline, with or without the
// init limiter, and the event intake and status routes are not.
func TestClientWriteDeadlineCoversPollAndEvalRoutes(t *testing.T) {
	env := st.EnvWithAllCredentials
	sdkKey, mobileKey, envID := env.Config.SDKKey, env.Config.MobileKey, env.Config.EnvID

	covered := []endpointTestParams{
		{"PHP all-flags poll", "GET", "/sdk/flags", nil, sdkKey, http.StatusOK, st.ExpectNoBody()},
		{"PHP single flag", "GET", "/sdk/flags/" + st.Flag1ServerSide.Flag.Key, nil, sdkKey, http.StatusOK, st.ExpectNoBody()},
		{"PHP single segment", "GET", "/sdk/segments/" + st.Segment1.Key, nil, sdkKey, http.StatusOK, st.ExpectNoBody()},
		{"FDv2 server poll", "GET", "/sdk/poll", nil, sdkKey, http.StatusOK, st.ExpectNoBody()},
		{"server-side evalx", "REPORT", "/sdk/evalx/context", basicContextJSON, sdkKey, http.StatusOK, st.ExpectNoBody()},
		{"JS evalx", "GET", "/sdk/evalx/$ENV/contexts/$DATA", basicContextJSON, envID, http.StatusOK, st.ExpectNoBody()},
		{"mobile evalx", "GET", "/msdk/evalx/contexts/$DATA", basicContextJSON, mobileKey, http.StatusOK, st.ExpectNoBody()},
		{"FDv2 client poll", "GET", "/sdk/poll/eval/$DATA", basicContextJSON, mobileKey, http.StatusOK, st.ExpectNoBody()},
	}
	uncovered := []endpointTestParams{
		{"status", "GET", "/status", nil, nil, http.StatusOK, st.ExpectNoBody()},
		// Events are not configured, so this is a 503, which is enough to show that the route
		// writes its response without a deadline.
		{"server-side events", "POST", "/bulk", []byte("[]"), sdkKey, http.StatusServiceUnavailable, st.ExpectNoBody()},
	}

	for _, limited := range []bool{false, true} {
		var config c.Config
		config.Environment = st.MakeEnvConfigs(env)
		floor, err := ct.NewOptIntGreaterThanZero(64 * 1024)
		require.NoError(t, err)
		config.Main.ClientWriteMinBytesPerSecond = floor
		if limited {
			config.Concurrency = c.ConcurrencyConfig{MaxConcurrent: optInt(4), MaxQueued: optInt(4)}
		}
		withStartedRelay(t, config, func(p relayTestParams) {
			for _, e := range covered {
				t.Run(e.name, func(t *testing.T) {
					rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
					p.relay.ServeHTTP(rec, e.request())
					assert.Equal(t, e.expectedStatus, rec.Code)
					assert.True(t, rec.armed(), "the response was written without a deadline (limiter=%t)", limited)
				})
			}
			for _, e := range uncovered {
				t.Run(e.name, func(t *testing.T) {
					rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
					p.relay.ServeHTTP(rec, e.request())
					assert.False(t, rec.armed(), "the response has a deadline (limiter=%t)", limited)
				})
			}
		})
	}
}
