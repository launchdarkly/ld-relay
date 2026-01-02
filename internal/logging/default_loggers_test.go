package logging

import (
	"os"
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
)

func TestDefaultLoggers(t *testing.T) {
	loggers := MakeDefaultLoggers()
	assert.Equal(t, ldlog.Info, loggers.GetMinLevel())
}

func TestIsJSONFormat(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"empty value", "", false},
		{"text format", "text", false},
		{"TEXT format", "TEXT", false},
		{"json format", "json", true},
		{"JSON format", "JSON", true},
		{"Json format", "Json", true},
		{"invalid format", "xml", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalValue := os.Getenv("LOG_FORMAT")
			defer func() {
				if originalValue == "" {
					os.Unsetenv("LOG_FORMAT")
				} else {
					os.Setenv("LOG_FORMAT", originalValue)
				}
			}()

			if tt.envValue == "" {
				os.Unsetenv("LOG_FORMAT")
			} else {
				os.Setenv("LOG_FORMAT", tt.envValue)
			}

			assert.Equal(t, tt.expected, isJSONFormat())
		})
	}
}
