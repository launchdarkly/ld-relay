package util

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

type errorJSON struct {
	Message string `json:"message"`
}

// ErrorJSONMsg returns a json-encoded error message
func ErrorJSONMsg(msg string) (j []byte) {
	j, _ = json.Marshal(errorJSON{Message: msg})
	return
}

// ErrorJSONMsgf returns a json-encoded error message using the printf formatter
func ErrorJSONMsgf(fmtStr string, args ...interface{}) []byte {
	return ErrorJSONMsg(fmt.Sprintf(fmtStr, args...))
}

// RedactedPlaceholder is substituted for any part of a URL that could contain a credential.
const RedactedPlaceholder = "xxxxx"

// RedactURL replaces the credential-bearing components of a URL string - the userinfo section, the
// query, and the fragment - with RedactedPlaceholder, while preserving the scheme, host, port and
// path so the result is still useful for diagnostics. Each component is replaced only when it is
// actually present. A URL that cannot be parsed is replaced entirely, since we cannot tell which
// parts of it are sensitive.
//
// The path is deliberately preserved, so the result is not safe for a URL that embeds a credential
// in a path segment.
func RedactURL(inputURL string) string {
	if inputURL == "" {
		return ""
	}
	parsed, err := url.Parse(inputURL)
	if err != nil || parsed == nil {
		return RedactedPlaceholder
	}
	redacted := *parsed
	// url.Parse reports a non-nil but empty Userinfo for "scheme://@host", which is not a credential.
	if redacted.User != nil && redacted.User.String() != "" {
		redacted.User = url.User(RedactedPlaceholder)
	}
	// A non-hierarchical URL ("scheme:rest", with no "//") keeps everything, userinfo included, in
	// Opaque. Redact only up to the last "@" rather than the whole thing: a bare "host:port" is also
	// reported as scheme plus opaque, and it must keep its port.
	if at := strings.LastIndex(redacted.Opaque, "@"); at >= 0 {
		redacted.Opaque = RedactedPlaceholder + redacted.Opaque[at:]
	}
	// ForceQuery means a trailing "?" with no query content, so there is nothing to redact.
	if redacted.RawQuery != "" {
		redacted.RawQuery = RedactedPlaceholder
	}
	if redacted.Fragment != "" {
		redacted.Fragment = RedactedPlaceholder
	}
	return redacted.String()
}

func DecompressGzipData(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return data, err
	}

	return io.ReadAll(reader)
}
