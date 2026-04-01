package autoconfigcache

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"strings"

	"github.com/launchdarkly/ld-relay/v8/config"
)

// resolveEncryptionKey returns a 32-byte key for AES-256 by deriving it via SHA-256.
// If CacheEncryptionKey is set, it is used as the input. Otherwise the AutoConfig key is used.
func resolveEncryptionKey(c config.Config) []byte {
	s := strings.TrimSpace(c.AutoConfig.CacheEncryptionKey)
	if s == "" {
		s = string(c.AutoConfig.Key)
	}
	h := sha256.Sum256([]byte(s))
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
