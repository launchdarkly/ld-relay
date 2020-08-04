package util

import (
	"encoding/json"
	"fmt"
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
