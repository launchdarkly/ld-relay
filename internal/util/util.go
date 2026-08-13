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

// RedactURL is equivalent to parsing a URL string and then calling Redacted() to
// replace passwords, if any, with xxxxx. We still support Go 1.14 so we can't use
// the actual URL.Redacted().
func RedactURL(inputURL string) string {
	if parsed, err := url.Parse(inputURL); err == nil {
		if parsed != nil && parsed.User != nil {
			if _, hasPW := parsed.User.Password(); hasPW {
				transformed := *parsed
				transformed.User = url.UserPassword(parsed.User.Username(), "xxxxx")
				return transformed.String()
			}
		}
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
