package autoconfig

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeStreamLogLine(t *testing.T) {
	assert.Equal(t, "plain text", sanitizeStreamLogLine("plain text"))

	// A newline would let upstream text end the line and start a forged one.
	assert.Equal(t, "error 401: denied FAKE Error: forged line",
		sanitizeStreamLogLine("error 401: denied\nFAKE Error: forged line"))
	assert.Equal(t, "a b c", sanitizeStreamLogLine("a\rb\x00c"))

	// Tabs are legible in a log and carry no line-ending risk.
	assert.Equal(t, "a\tb", sanitizeStreamLogLine("a\tb"))

	long := sanitizeStreamLogLine(strings.Repeat("x", maxStreamLogLineLength+50))
	assert.Len(t, long, maxStreamLogLineLength+len("... (truncated)"))
	assert.True(t, strings.HasSuffix(long, "... (truncated)"))
}

// The response body of a rejected request reaches the log through the SSE library's error text.
// It comes from whatever answered the request, so it must not be able to forge a log line.
func TestResponseBodyCannotForgeALogLine(t *testing.T) {
	const canary = "CANARY-BODY"
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(canary + "\nFAKE Error: forged log line\n" + strings.Repeat("padding ", 60)))
	}
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithStreamHandler(t, http.HandlerFunc(handler), stream, noopTestCache{},
		func(p streamManagerTestParams) {
			p.streamManager.extendedRetryDelay = time.Millisecond
			p.streamManager.ignoreConnectionErrors = true // stay alive long enough to log
			p.streamManager.Start()

			require.Eventually(t, func() bool {
				return hasAnyMessage(p.mockLog, ldlog.Info, "Connection failed")
			}, 2*time.Second, 10*time.Millisecond, "expected the library's retry line")

			for _, line := range p.mockLog.GetOutput(ldlog.Info) {
				if !strings.Contains(line, "Connection failed") {
					continue
				}
				assert.NotContains(t, line, "\n", "the body must not be able to end the line")
				assert.LessOrEqual(t, len(line), maxStreamLogLineLength+len("... (truncated)")+40,
					"an unbounded body must not reach the log in full")
			}
		})
}
