package autoconfig

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/httpconfig"
	"github.com/launchdarkly/ld-relay/v8/internal/retry"
)

// Every status in the slow-retry class keeps Relay running and keeps it trying. The class
// decides how long to wait, and nothing else: a misconfigured stream URI or a load balancer
// answering 404 mid-deploy must not take a fleet down, and neither must a rejected key.
func TestUnexpectedStatusKeepsRelayRunning(t *testing.T) {
	for _, status := range []int{404, 405, 410, 418, 451} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			// These are all in the slow-retry class, which is the point: the class picks the
			// delay and never decides whether to keep trying.
			require.Equal(t, retry.Unexpected, retry.ClassifyHTTPStatus(status))

			handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(status))
			_, stream := httphelpers.SSEHandler(nil)
			defer stream.Close()

			streamManagerTestWithStreamHandler(t, handler, stream, noopTestCache{}, func(p streamManagerTestParams) {
				p.streamManager.extendedRetryDelay = time.Millisecond

				readyCh := p.streamManager.Start()

				// It keeps trying rather than reporting a failure.
				helpers.RequireValue(t, requestsCh, time.Second, "expected the first attempt")
				helpers.RequireValue(t, requestsCh, time.Second, "expected a retry")

				if !helpers.AssertNoMoreValues(t, readyCh, 300*time.Millisecond,
					"Relay reported a fatal error for a status that is not a rejected credential") {
					t.FailNow()
				}
				p.mockLog.AssertMessageMatch(t, false, ldlog.Error, "Invalid auto-configuration key")
			})
		})
	}
}

// A certificate failure is in the slow-retry class too, and likewise must not stop Relay. A
// machine with a skewed clock, or a TLS-terminating proxy mid-cert-rollover, produces this and
// resolves without Relay's involvement.
func TestCertificateFailureDoesNotStopRelay(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
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
	sm.extendedRetryDelay = time.Millisecond

	readyCh := sm.Start()
	if !helpers.AssertNoMoreValues(t, readyCh, time.Second,
		"Relay reported a fatal error for a certificate failure") {
		t.FailNow()
	}

	// A certificate failure is a transport failure, and no transport failure is unexpected, so
	// it must stay on the short delays. Waiting minutes would not help: the failure resolves
	// the moment an operator fixes the certificate.
	mockLog.AssertMessageMatch(t, false, ldlog.Info, "engaging extended backoff")
	mockLog.AssertMessageMatch(t, true, ldlog.Warn, "Unexpected error on auto-configuration stream")
}
