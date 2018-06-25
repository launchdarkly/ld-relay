package util

import (
	"encoding/json"
	"fmt"
)

type errorJson struct {
	Message string `json:"message"`
}

// ErrorJsonMsg returns a json-encoded error message
func ErrorJsonMsg(msg string) (j []byte) {
	j, _ = json.Marshal(errorJson{Message: msg})
	return
}

// ErrorJsonMsgf returns a json-encoded error message using the printf formatter
func ErrorJsonMsgf(fmtStr string, args ...interface{}) []byte {
	return ErrorJsonMsg(fmt.Sprintf(fmtStr, args...))
}
