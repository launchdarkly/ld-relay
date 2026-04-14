package autoconfigcache

import (
	"crypto/sha256"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := sha256.Sum256([]byte("test-key"))
	plaintext := []byte("hello, world")

	ciphertext, err := encrypt(plaintext, key[:])
	require.NoError(t, err)

	// Ciphertext should differ from plaintext.
	assert.NotEqual(t, plaintext, ciphertext)

	decrypted, err := decrypt(ciphertext, key[:])
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestEncryptProducesDifferentCiphertextEachTime(t *testing.T) {
	key := sha256.Sum256([]byte("test-key"))
	plaintext := []byte("same input")

	ct1, err := encrypt(plaintext, key[:])
	require.NoError(t, err)

	ct2, err := encrypt(plaintext, key[:])
	require.NoError(t, err)

	// Each encryption uses a random nonce, so ciphertexts should differ.
	assert.NotEqual(t, ct1, ct2)
}

func TestDecryptFailsWithWrongKey(t *testing.T) {
	key1 := sha256.Sum256([]byte("key-1"))
	key2 := sha256.Sum256([]byte("key-2"))

	ciphertext, err := encrypt([]byte("secret"), key1[:])
	require.NoError(t, err)

	_, err = decrypt(ciphertext, key2[:])
	assert.Error(t, err)
}

func TestDecryptFailsWithTruncatedCiphertext(t *testing.T) {
	key := sha256.Sum256([]byte("key"))
	_, err := decrypt([]byte("short"), key[:])
	assert.Error(t, err)
}

func TestResolveEncryptionKeyUsesCacheEncryptionKey(t *testing.T) {
	c := config.Config{}
	c.AutoConfig.Key = "auto-config-key"
	c.AutoConfig.CacheEncryptionKey = "my-custom-key"

	key := resolveEncryptionKey(c)

	expected := sha256.Sum256([]byte("my-custom-key"))
	assert.Equal(t, expected[:], key)
}

func TestResolveEncryptionKeyFallsBackToAutoConfigKey(t *testing.T) {
	c := config.Config{}
	c.AutoConfig.Key = "auto-config-key"

	key := resolveEncryptionKey(c)

	expected := sha256.Sum256([]byte("auto-config-key"))
	assert.Equal(t, expected[:], key)
}
