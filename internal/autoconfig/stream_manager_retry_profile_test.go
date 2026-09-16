package autoconfig

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
)

// TestExtendedDelaysDoubleFromTheExtendedBase checks that engaging the extended profile really
// slows retries down, which is the point of the change and the part no other test covers:
// removing the profile activation leaves every other test in the package green.
//
// The activation has to survive repeated passes through eventsource's
// CanRetryFirstConnection(-1) loop, and the per-profile attempt counter has to double from the
// extended base rather than the normal one. Both hold.
func TestExtendedDelaysDoubleFromTheExtendedBase(t *testing.T) {
	const base = 20 * time.Millisecond

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
	}))
	defer server.Close()

	mockLog := ldlogtest.NewMockLog()
	defer mockLog.DumpIfTestFailed(t)
	mockLog.Loggers.SetMinLevel(ldlog.Debug)

	httpConfig, err := httpconfig.NewHTTPConfig(config.ProxyConfig{}, config.HTTPConfig{}, nil, "", mockLog.Loggers)
	require.NoError(t, err)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	sm := NewStreamManager(
		testConfigKey, serverURL, newTestMessageHandler(), httpConfig,
		time.Millisecond, rpacProtocolVersion, mockLog.Loggers, noopTestCache{},
	)
	defer sm.Close()
	sm.extendedRetryDelay = base

	sm.Start()

	var delays []time.Duration
	require.Eventually(t, func() bool {
		delays = reconnectDelays(mockLog)
		return len(delays) >= 4
	}, 5*time.Second, 10*time.Millisecond, "expected at least 4 logged delays")

	t.Logf("first four extended delays: %v", delays[:4])
	// Jitter removes up to half, so attempt n's delay is in [base*2^(n-1)/2, base*2^(n-1)].
	for i := 0; i < 4; i++ {
		want := time.Duration(1<<uint(i)) * base
		assert.GreaterOrEqual(t, delays[i], want/2, "attempt %d", i+1)
		assert.LessOrEqual(t, delays[i], want, "attempt %d", i+1)
	}
	// The normal profile's 1ms base is never used again.
	assert.Greater(t, delays[3], delays[0])
}
