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
