package util

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
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
// actually present.
//
// The path is deliberately preserved, so the result is not safe for a URL that embeds a credential
// in a path segment.
func RedactURL(inputURL string) string {
	if inputURL == "" {
		return ""
	}
	parsed, err := url.Parse(inputURL)
	if err != nil || parsed == nil {
		return redactUnparseable(inputURL)
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

// redactUnparseable handles a value url.Parse rejects.
//
// A bare "host:port" whose host is numeric or bracketed is one of these: url.Parse refuses
// "127.0.0.1:8500" and "[::1]:8500" because the first path segment cannot contain a colon. Consul's
// address is exactly that shape, and its default is 127.0.0.1:8500, so replacing the whole value
// would throw away an address that holds no credential at all.
//
// The three characters below introduce the only components RedactURL redacts: "@" ends a userinfo
// section, "?" begins a query, and "#" begins a fragment. A value containing none of them is a
// scheme, host, port and path, all of which RedactURL preserves when it can parse them, so
// preserving them here is the same decision rather than a weaker one. Anything else is replaced
// entirely, because without a parse we cannot tell which part of it is sensitive.
func redactUnparseable(inputURL string) string {
	if strings.ContainsAny(inputURL, "@?#") {
		return RedactedPlaceholder
	}
	return inputURL
}

func DecompressGzipData(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return data, err
	}

	return io.ReadAll(reader)
}

// SanitizeUTF8 removes invalid UTF-8 byte sequences from v.
//
// OpenTelemetry serializes attribute values into OTLP protobuf string fields, and proto3 requires
// those to be valid UTF-8. An invalid byte does not merely spoil the value: proto.Marshal fails, so
// the entire export batch is dropped rather than just the offending span or data point. For metrics
// that is unrecoverable, because the poisoned series is cumulative and gets re-collected on every
// interval until the process restarts.
//
// This is reachable from ordinary request data. HTTP header values are not restricted to ASCII --
// RFC 7230 permits obs-text, and Go's parser passes those bytes through unchanged -- and a
// percent-encoded URL path decodes to arbitrary bytes. Any attribute value derived from either has to
// pass through here.
func SanitizeUTF8(v string) string {
	if utf8.ValidString(v) {
		return v
	}
	return strings.ToValidUTF8(v, "")
}
