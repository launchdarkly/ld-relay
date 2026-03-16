package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseHeaders(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:     "single header",
			input:    "api-key=secret",
			expected: map[string]string{"api-key": "secret"},
		},
		{
			name:     "multiple headers",
			input:    "api-key=secret,env=prod",
			expected: map[string]string{"api-key": "secret", "env": "prod"},
		},
		{
			name:     "value containing equals sign",
			input:    "Authorization=Basic dXNlcjpwYXNz",
			expected: map[string]string{"Authorization": "Basic dXNlcjpwYXNz"},
		},
		{
			name:     "whitespace trimmed",
			input:    " api-key = secret , env = prod ",
			expected: map[string]string{"api-key": "secret", "env": "prod"},
		},
		{
			name:     "trailing comma",
			input:    "api-key=secret,",
			expected: map[string]string{"api-key": "secret"},
		},
		{
			name:     "malformed entry without equals is skipped",
			input:    "api-key=secret,malformed,env=prod",
			expected: map[string]string{"api-key": "secret", "env": "prod"},
		},
		{
			name:     "value with equals signs",
			input:    "token=abc=def=ghi",
			expected: map[string]string{"token": "abc=def=ghi"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseHeaders(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
