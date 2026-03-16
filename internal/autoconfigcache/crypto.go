package autoconfigcache

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/config"
)

const aesKeySize = 32

// resolveEncryptionKey returns a 32-byte key for AES-256. If CacheEncryptionKey is set, it must be
// 32 bytes or base64-encoded 32 bytes. If not set, the AutoConfig key is used (SHA-256 derived).
func resolveEncryptionKey(c config.Config) ([]byte, error) {
	s := strings.TrimSpace(c.AutoConfig.CacheEncryptionKey)
	if s != "" {
		return decodeEncryptionKey(s)
	}
	// Use AutoConfig key as the encryption key (derive 32 bytes via SHA-256).
	return deriveKeyFromAutoconfigKey(string(c.AutoConfig.Key)), nil
}

// decodeEncryptionKey returns a 32-byte key for AES-256. It accepts raw 32-byte string or base64.
func decodeEncryptionKey(s string) ([]byte, error) {
	raw := []byte(s)
	if len(raw) == aesKeySize {
		return raw, nil
	}
	dec, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("AUTO_CONFIG_CACHE_ENCRYPTION_KEY must be 32 bytes or base64-encoded 32 bytes")
	}
	if len(dec) != aesKeySize {
		return nil, errors.New("AUTO_CONFIG_CACHE_ENCRYPTION_KEY must decode to 32 bytes for AES-256")
	}
	return dec, nil
}

// deriveKeyFromAutoconfigKey produces a 32-byte key from the AutoConfig key string using SHA-256.
func deriveKeyFromAutoconfigKey(autoconfigKey string) []byte {
	h := sha256.Sum256([]byte(autoconfigKey))
	return h[:]
}

func encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
