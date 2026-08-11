package tracing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeAttributeValue(t *testing.T) {
	assert.Equal(t, "abc", SanitizeAttributeValue("abc"))
	assert.Equal(t, "not-provided", SanitizeAttributeValue(""))
	assert.Equal(t, "not-provided", SanitizeAttributeValue("   "))
	assert.Equal(t, "react_2.0.0", SanitizeAttributeValue("react/2.0.0"))
	assert.Equal(t, "My Project_My Env", SanitizeAttributeValue("My Project/My Env"))

	// An auto-configured environment with no project name computes " Production" as its display
	// name; without trimming it would be a separate series from "Production" but render identically.
	assert.Equal(t, "Production", SanitizeAttributeValue(" Production"))
	assert.Equal(t, "Production", SanitizeAttributeValue("Production\t"))
}
