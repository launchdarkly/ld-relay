// AutoConfig cache encryption.
//
// The cache stores environment metadata (including SDK keys and mobile keys) in Redis
// or DynamoDB. Encryption at rest provides defense in depth against unauthorized read
// access to the persistent store.
//
// Uses AES-256-GCM with a random nonce per item and HKDF-SHA256 for key derivation
// from the configured CacheEncryptionKey (or the AutoConfig key as a fallback).
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
	"golang.org/x/crypto/hkdf"
)

const aesKeySize = 32

// resolveEncryptionKey derives a 32-byte AES-256 key using HKDF (RFC 5869).
// If CacheEncryptionKey is set, it is used as the input keying material. Otherwise the AutoConfig key is used.
func resolveEncryptionKey(c config.Config) []byte {
	s := strings.TrimSpace(c.AutoConfig.CacheEncryptionKey)
	if s == "" {
		s = string(c.AutoConfig.Key)
	}
	return deriveKey([]byte(s))
}

// deriveKey uses HKDF-SHA256 to derive a 32-byte AES-256 key from the input keying material.
func deriveKey(ikm []byte) []byte {
	r := hkdf.New(sha256.New, ikm, nil, []byte("ld-relay autoconfig cache"))
	key := make([]byte, aesKeySize)
	// hkdf.Read only fails if the output length exceeds 255*HashLen, which is not possible for 32 bytes.
	_, _ = io.ReadFull(r, key)
	return key
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
