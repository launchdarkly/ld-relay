package autoconfig

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
)

// maxStreamLogLineLength caps a line forwarded from the SSE library. The text can embed an
// error response body of any size, and a log line is not the place to reproduce one.
const maxStreamLogLineLength = 200

// streamLogger forwards the SSE library's log output to Relay's logger after making it safe to
// write to a log.
//
// The library reports a failed connection as "Connection failed (%s), retrying in ... secs",
// where the error text for a rejected request embeds the response body verbatim. That body
// comes from whatever answered the request, which may be an intermediary rather than
// LaunchDarkly. Passing it through unaltered lets it end a line and start another, so it can
// forge log entries; and an error page can be arbitrarily long.
type streamLogger struct {
	dest ldlog.BaseLogger
}

func (l streamLogger) Println(values ...interface{}) {
	l.dest.Println(sanitizeStreamLogLine(fmt.Sprint(values...)))
}

func (l streamLogger) Printf(format string, values ...interface{}) {
	l.dest.Println(sanitizeStreamLogLine(fmt.Sprintf(format, values...)))
}

// sanitizeStreamLogLine collapses every control character, so the text cannot span lines, and
// truncates it to a length a log can reasonably carry.
func sanitizeStreamLogLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return ' '
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > maxStreamLogLineLength {
		return s[:maxStreamLogLineLength] + "... (truncated)"
	}
	return s
}
