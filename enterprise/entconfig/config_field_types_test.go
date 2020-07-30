package entconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAutoConfigKey(t *testing.T) {
	assert.Equal(t, "123", AutoConfigKey("123").GetAuthorizationHeaderValue())
}
