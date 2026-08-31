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
	assert.Equal(t, "", RedactURL(""))
	assert.Equal(t, "redis://redishost:3000", RedactURL("redis://redishost:3000"))
	assert.Equal(t, "redis://redishost:3000/1", RedactURL("redis://redishost:3000/1"))
	assert.Equal(t, "redis://xxxxx@redishost", RedactURL("redis://username@redishost"))
	assert.Equal(t, "redis://xxxxx@redishost", RedactURL("redis://very-secret-token@redishost"))
	assert.Equal(t, "redis://xxxxx@redishost", RedactURL("redis://username:very-secret-password@redishost"))
	assert.Equal(t, "redis://redishost?xxxxx", RedactURL("redis://redishost?password=very-secret-password"))
	assert.Equal(t, "https://xxxxx@dbhost:8000/path?xxxxx#xxxxx",
		RedactURL("https://username:very-secret-password@dbhost:8000/path?token=very-secret-token#very-secret-fragment"))
	assert.Equal(t, "xxxxx", RedactURL("redis://user:very-secret-password@host:not-a-port"))
}
