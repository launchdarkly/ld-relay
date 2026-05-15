package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONLogger_Printf(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf)

	logger.Printf("INFO: test message %s", "value")

	var entry JSONLogEntry
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "INFO", entry.Level)
	assert.Equal(t, "test message value", entry.Message)
	assert.NotEmpty(t, entry.Timestamp)
}

func TestJSONLogger_Println(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf)

	logger.Println("DEBUG: debug message")

	var entry JSONLogEntry
	err := json.Unmarshal(buf.Bytes(), &entry)
	require.NoError(t, err)

	assert.Equal(t, "DEBUG", entry.Level)
	assert.Equal(t, "debug message", entry.Message)
}

func TestJSONLogger_AllLevels(t *testing.T) {
	tests := []struct {
		input         string
		expectedLevel string
		expectedMsg   string
	}{
		{"DEBUG: debug message", "DEBUG", "debug message"},
		{"INFO: info message", "INFO", "info message"},
		{"WARN: warn message", "WARN", "warn message"},
		{"ERROR: error message", "ERROR", "error message"},
		{"no level prefix", "INFO", "no level prefix"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewJSONLogger(&buf)

			logger.Println(tt.input)

			var entry JSONLogEntry
			err := json.Unmarshal(buf.Bytes(), &entry)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedLevel, entry.Level)
			assert.Equal(t, tt.expectedMsg, entry.Message)
		})
	}
}

func TestJSONLogger_MultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf)

	logger.Println("INFO: first message")
	logger.Println("ERROR: second message")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)

	var entry1, entry2 JSONLogEntry
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry1))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &entry2))

	assert.Equal(t, "INFO", entry1.Level)
	assert.Equal(t, "first message", entry1.Message)
	assert.Equal(t, "ERROR", entry2.Level)
	assert.Equal(t, "second message", entry2.Message)
}

func TestJSONLogger_TimestampFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf)

	logger.Println("INFO: test")

	var entry JSONLogEntry
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))

	// Verify timestamp is in RFC3339Nano format (contains T and Z or timezone offset)
	assert.Contains(t, entry.Timestamp, "T")
	// Should contain either Z for UTC or + for timezone offset
	assert.True(t, strings.Contains(entry.Timestamp, "Z") || strings.Contains(entry.Timestamp, "+") || strings.Contains(entry.Timestamp, "-"))
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input          string
		expectedLevel  string
		expectedRemain string
	}{
		{"DEBUG: test message", "DEBUG", "test message"},
		{"INFO: test message", "INFO", "test message"},
		{"WARN: test message", "WARN", "test message"},
		{"ERROR: test message", "ERROR", "test message"},
		{"unknown message", "INFO", "unknown message"},
		{"TRACE: not a valid level", "INFO", "TRACE: not a valid level"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			level, remainder := parseLogLevel(tt.input)
			assert.Equal(t, tt.expectedLevel, level)
			assert.Equal(t, tt.expectedRemain, remainder)
		})
	}
}
