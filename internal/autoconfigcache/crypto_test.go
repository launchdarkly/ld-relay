package autoconfigcache

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := deriveKey([]byte("test-key"))
	plaintext := []byte("hello, world")

	ciphertext, err := encrypt(plaintext, key)
	require.NoError(t, err)

	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := decrypt(ciphertext, key)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptProducesDifferentCiphertextEachTime(t *testing.T) {
	key := deriveKey([]byte("test-key"))
	plaintext := []byte("same input")

	ct1, err := encrypt(plaintext, key)
	require.NoError(t, err)

	ct2, err := encrypt(plaintext, key)
	require.NoError(t, err)

	// Each encryption uses a random nonce, so ciphertexts should differ.
	assert.NotEqual(t, ct1, ct2)
}

func TestDecryptFailsWithWrongKey(t *testing.T) {
	key1 := deriveKey([]byte("key-1"))
	key2 := deriveKey([]byte("key-2"))

	ciphertext, err := encrypt([]byte("secret"), key1)
	require.NoError(t, err)

	_, err = decrypt(ciphertext, key2)
	assert.Error(t, err)
}

func TestDecryptFailsWithTruncatedCiphertext(t *testing.T) {
	key := deriveKey([]byte("key"))
	_, err := decrypt([]byte("short"), key)
	assert.Error(t, err)
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	key := deriveKey([]byte("test-key"))
	plaintext := []byte("sensitive environment data including SDK keys")

	ciphertext, err := encrypt(plaintext, key)
	require.NoError(t, err)

	// Ciphertext layout: nonce (12 bytes) || encrypted body || GCM tag (16 bytes).
	// Flipping a bit anywhere in that blob must cause Open() to fail.
	const nonceSize = 12
	const tagSize = 16
	bodyStart := nonceSize
	bodyEnd := len(ciphertext) - tagSize
	require.Greater(t, bodyEnd, bodyStart, "ciphertext should have a non-empty body")

	cases := []struct {
		name  string
		index int
	}{
		{"flip bit in nonce", 0},
		{"flip bit in nonce (last byte)", nonceSize - 1},
		{"flip bit in encrypted body", bodyStart},
		{"flip bit in middle of body", bodyStart + (bodyEnd-bodyStart)/2},
		{"flip bit in GCM auth tag", bodyEnd},
		{"flip bit in last byte of tag", len(ciphertext) - 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := append([]byte(nil), ciphertext...)
			tampered[tc.index] ^= 0x01

			_, err := decrypt(tampered, key)
			assert.Error(t, err, "decrypt must reject tampered ciphertext at index %d", tc.index)
		})
	}
}

func TestDecryptRejectsAppendedBytes(t *testing.T) {
	key := deriveKey([]byte("test-key"))
	ciphertext, err := encrypt([]byte("payload"), key)
	require.NoError(t, err)

	tampered := append(append([]byte(nil), ciphertext...), 0x00, 0x01, 0x02)
	_, err = decrypt(tampered, key)
	assert.Error(t, err)
}

func TestDecryptRejectsTruncatedTag(t *testing.T) {
	key := deriveKey([]byte("test-key"))
	ciphertext, err := encrypt([]byte("payload"), key)
	require.NoError(t, err)

	// Lop off the last byte of the GCM tag — auth must fail.
	tampered := ciphertext[:len(ciphertext)-1]
	_, err = decrypt(tampered, key)
	assert.Error(t, err)
}

func TestDeriveKeyProducesCorrectLength(t *testing.T) {
	key := deriveKey([]byte("any input"))
	assert.Len(t, key, aesKeySize)
}

func TestDeriveKeyIsDeterministic(t *testing.T) {
	k1 := deriveKey([]byte("same-input"))
	k2 := deriveKey([]byte("same-input"))
	assert.Equal(t, k1, k2)
}

func TestDeriveKeyDiffersForDifferentInputs(t *testing.T) {
	k1 := deriveKey([]byte("input-a"))
	k2 := deriveKey([]byte("input-b"))
	assert.NotEqual(t, k1, k2)
}

func TestResolveEncryptionKeyUsesCacheEncryptionKey(t *testing.T) {
	c := config.Config{}
	c.AutoConfig.Key = "auto-config-key"
	c.AutoConfig.CacheEncryptionKey = "my-custom-key"

	key := resolveEncryptionKey(c)

	expected := deriveKey([]byte("my-custom-key"))
	assert.Equal(t, expected, key)
}

func TestResolveEncryptionKeyFallsBackToAutoConfigKey(t *testing.T) {
	c := config.Config{}
	c.AutoConfig.Key = "auto-config-key"

	key := resolveEncryptionKey(c)

	expected := deriveKey([]byte("auto-config-key"))
	assert.Equal(t, expected, key)
}
