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
	assert.Equal(t, "error 500: denied FAKE Error: forged line",
		sanitizeStreamLogLine("error 500: denied\nFAKE Error: forged line"))
	assert.Equal(t, "a b c", sanitizeStreamLogLine("a\rb\x00c"))

	// Tabs are legible in a log and carry no line-ending risk.
	assert.Equal(t, "a\tb", sanitizeStreamLogLine("a\tb"))

	long := sanitizeStreamLogLine(strings.Repeat("x", maxStreamLogLineLength+50))
	assert.Len(t, long, maxStreamLogLineLength+len("... (truncated)"))
	assert.True(t, strings.HasSuffix(long, "... (truncated)"))
}

// The response body of a failed request reaches the log through the SSE library's error text.
// It comes from whatever answered the request, which may be an intermediary rather than
// LaunchDarkly, so it must not be able to forge a log line or fill the log.
//
// The handler answers 500 because the library only logs its retry line for a failure the
// stream retries. A rejected key closes the stream instead, so it does not reach this line.
func TestResponseBodyCannotForgeALogLine(t *testing.T) {
	const canary = "CANARY-BODY"
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(canary + "\nFAKE Error: forged log line\n" + strings.Repeat("padding ", 60)))
	}
	_, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()

	streamManagerTestWithStreamHandler(t, http.HandlerFunc(handler), stream, noopTestCache{},
		func(p streamManagerTestParams) {
			p.streamManager.Start()

			var lines []string
			require.Eventually(t, func() bool {
				lines = nil
				for _, line := range p.mockLog.GetOutput(ldlog.Info) {
					if strings.Contains(line, "Connection failed") {
						lines = append(lines, line)
					}
				}
				return len(lines) > 0
			}, 2*time.Second, 10*time.Millisecond, "expected the library's retry line")

			for _, line := range lines {
				assert.NotContains(t, line, "\n", "the body must not be able to end the line")
				assert.LessOrEqual(t, len(line), maxStreamLogLineLength+len("... (truncated)")+40,
					"an unbounded body must not reach the log in full")
			}
		})
}
