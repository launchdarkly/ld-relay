// The cache stores environment metadata including SDK keys, mobile keys, and project
// configuration. These credentials could be used to connect to LaunchDarkly or receive
// flag data if exposed.
//
// The persistent store (Redis or DynamoDB) should already be access-controlled, but
// encrypting cached data at rest provides defense in depth: if an attacker gains read
// access to the store (e.g. via a misconfigured security group, leaked DB credentials,
// or a shared multi-tenant database), they cannot extract SDK keys or other secrets
// without also knowing the encryption key.
//
// Encryption uses AES-256-GCM with a random nonce per item, providing both
// confidentiality and integrity. The encryption key is derived via SHA-256 from
// either a user-provided CacheEncryptionKey or the AutoConfig key.
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
