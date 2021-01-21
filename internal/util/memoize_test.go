package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringMemoizer(t *testing.T) {
	computedCount := 0
	computeFn := func() string {
		computedCount++
		return fmt.Sprintf("value %d", computedCount)
	}
	m := NewStringMemoizer(computeFn)
	assert.Equal(t, 0, computedCount)
	assert.Equal(t, "value 1", m.Get())
	assert.Equal(t, "value 1", m.Get())
	assert.Equal(t, 1, computedCount)
}
