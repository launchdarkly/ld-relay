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

	// A non-hierarchical "scheme:rest" URL keeps its userinfo in URL.Opaque. The config layer accepts
	// these (url.IsAbs is true), so a missing "//" must not publish the credential verbatim.
	assert.Equal(t, "redis:xxxxx@redishost:6379", RedactURL("redis:very-secret-password@redishost:6379"))
	assert.Equal(t, "mailto:xxxxx@example.com", RedactURL("mailto:secret@example.com"))

	// url.Parse reports a bare "host:port" as scheme plus opaque, so redacting the whole opaque
	// body would destroy the port. Consul's dbServer is exactly this shape.
	assert.Equal(t, "consul.example.com:8500", RedactURL("consul.example.com:8500"))
	assert.Equal(t, "my-host", RedactURL("my-host"))

	// A host:port whose host is numeric or bracketed is rejected by url.Parse outright: the first
	// path segment cannot contain a colon, and a digit or "[" cannot begin a scheme. These hold no
	// credential, and Consul's default address is one of them, so they survive verbatim rather than
	// being replaced. Only the letter-starting forms above take the scheme-plus-opaque path, which
	// is why they alone did not cover this.
	assert.Equal(t, "127.0.0.1:8500", RedactURL("127.0.0.1:8500"))
	assert.Equal(t, "10.0.0.5:6379", RedactURL("10.0.0.5:6379"))
	assert.Equal(t, "[::1]:8500", RedactURL("[::1]:8500"))
	assert.Equal(t, "192.168.1.1", RedactURL("192.168.1.1"))

	// An unparseable value that could be hiding a credential is still replaced whole, because
	// without a parse there is no way to tell which part of it is sensitive.
	assert.Equal(t, "xxxxx", RedactURL("redis://user:very-secret-password@127.0.0.1:not-a-port"))
	assert.Equal(t, "xxxxx", RedactURL("127.0.0.1:8500?token=very-secret-token"))
	assert.Equal(t, "xxxxx", RedactURL("127.0.0.1:8500#very-secret-fragment"))

	// An empty userinfo is not a credential, so do not claim to have redacted one.
	assert.Equal(t, "redis://@redishost:6379", RedactURL("redis://@redishost:6379"))

	// ForceQuery: a trailing "?" carries no query content, so there is nothing to redact.
	assert.Equal(t, "https://dbhost/path?", RedactURL("https://dbhost/path?"))

	// The path is deliberately preserved. This is a documented limitation, not an oversight.
	assert.Equal(t, "https://dbhost/v1/secret-in-path", RedactURL("https://dbhost/v1/secret-in-path"))

	// Redacting an already-redacted URL must not corrupt it.
	redactedOnce := RedactURL("https://username:very-secret-password@dbhost:8000/path?token=t#frag")
	assert.Equal(t, "https://xxxxx@dbhost:8000/path?xxxxx#xxxxx", redactedOnce)
	assert.Equal(t, redactedOnce, RedactURL(redactedOnce))
}
