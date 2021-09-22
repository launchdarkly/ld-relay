package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorJSONMsg(t *testing.T) {
	assert.Equal(t, `{"message":"sorry"}`, string(ErrorJSONMsg("sorry")))
	assert.Equal(t, `{"message":"bad thing"}`, string(ErrorJSONMsgf("bad %s", "thing")))
}

func TestRedactURL(t *testing.T) {
	assert.Equal(t, "redis://redishost:3000", RedactURL("redis://redishost:3000"))
	assert.Equal(t, "redis://redishost:3000/1", RedactURL("redis://redishost:3000/1"))
	assert.Equal(t, "redis://username@redishost", RedactURL("redis://username@redishost"))
	assert.Equal(t, "redis://username:xxxxx@redishost", RedactURL("redis://username:very-secret-password@redishost"))
}
