package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorJSONMsg(t *testing.T) {
	assert.Equal(t, `{"message":"sorry"}`, string(ErrorJSONMsg("sorry")))
	assert.Equal(t, `{"message":"bad thing"}`, string(ErrorJSONMsgf("bad %s", "thing")))
}
