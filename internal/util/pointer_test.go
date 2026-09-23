package util

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPtrOrNil(t *testing.T) {
	t.Run("empty string yields nil", func(t *testing.T) {
		assert.Nil(t, PtrOrNil(""))
	})

	t.Run("non-empty string yields pointer to value", func(t *testing.T) {
		p := PtrOrNil("default")
		require.NotNil(t, p)
		assert.Equal(t, "default", *p)
	})

	t.Run("zero time yields nil", func(t *testing.T) {
		assert.Nil(t, PtrOrNil(time.Time{}))
	})

	t.Run("non-zero time yields pointer to value", func(t *testing.T) {
		now := time.Unix(1000, 0)
		p := PtrOrNil(now)
		require.NotNil(t, p)
		assert.Equal(t, now, *p)
	})
}
