package util

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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

// RedactURL removes any credential-bearing components of a URL string - the entire userinfo
// section, the query, and the fragment - while preserving the scheme, host, port, and path so
// the result is still useful for diagnostics. A URL that cannot be parsed is replaced entirely,
// since we cannot tell which parts of it are sensitive.
func RedactURL(inputURL string) string {
	if inputURL == "" {
		return ""
	}
	parsed, err := url.Parse(inputURL)
	if err != nil || parsed == nil {
		return RedactedPlaceholder
	}
	redacted := *parsed
	if redacted.User != nil {
		redacted.User = url.User(RedactedPlaceholder)
	}
	if redacted.RawQuery != "" || redacted.ForceQuery {
		redacted.RawQuery = RedactedPlaceholder
		redacted.ForceQuery = false
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
