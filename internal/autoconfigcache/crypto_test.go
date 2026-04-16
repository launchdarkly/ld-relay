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
