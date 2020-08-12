package sharedtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

// AssertJSONPathMatch checks for a value within a nested JSON data structure.
func AssertJSONPathMatch(t *testing.T, expected interface{}, inValue ldvalue.Value, path ...string) {
	expectedValue := ldvalue.CopyArbitraryValue(expected)
	value := inValue
	for _, p := range path {
		value = value.GetByKey(p)
	}
	if !expectedValue.Equal(value) {
		assert.Fail(
			t,
			"did not find expected JSON value",
			"at path [%s] in %s\nexpected: %s\nfound: %s",
			strings.Join(path, "."),
			inValue,
			expectedValue,
			value,
		)
	}
}
