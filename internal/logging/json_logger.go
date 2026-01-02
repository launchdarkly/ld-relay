package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// JSONLogEntry represents a structured log entry.
type JSONLogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// JSONLogger implements ldlog.BaseLogger and outputs JSON-structured logs.
type JSONLogger struct {
	writer io.Writer
	mu     sync.Mutex
}

// NewJSONLogger creates a new JSONLogger that writes to the given writer.
func NewJSONLogger(w io.Writer) *JSONLogger {
	return &JSONLogger{writer: w}
}

// Printf implements ldlog.BaseLogger. It parses the level prefix from the format
// and outputs a JSON-structured log line.
func (l *JSONLogger) Printf(format string, values ...interface{}) {
	message := fmt.Sprintf(format, values...)
	l.output(message)
}

// Println implements ldlog.BaseLogger. It parses the level prefix from the message
// and outputs a JSON-structured log line.
func (l *JSONLogger) Println(values ...interface{}) {
	message := fmt.Sprint(values...)
	l.output(message)
}

func (l *JSONLogger) output(message string) {
	level, msg := parseLogLevel(message)

	entry := JSONLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Message:   strings.TrimSpace(msg),
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		// Fallback to plain output if JSON marshaling fails
		fmt.Fprintln(l.writer, message)
		return
	}

	fmt.Fprintln(l.writer, string(data))
}

// parseLogLevel extracts the log level prefix from a message.
// The ldlog library prepends "DEBUG: ", "INFO: ", "WARN: ", or "ERROR: " to messages.
func parseLogLevel(message string) (level, remainder string) {
	prefixes := []string{"DEBUG: ", "INFO: ", "WARN: ", "ERROR: "}
	for _, prefix := range prefixes {
		if strings.HasPrefix(message, prefix) {
			return strings.TrimSuffix(prefix, ": "), strings.TrimPrefix(message, prefix)
		}
	}
	// If no level prefix found, default to INFO
	return "INFO", message
}
